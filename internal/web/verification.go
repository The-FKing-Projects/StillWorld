package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/auth"
)

type verificationService interface {
	EmailVerificationStatus(context.Context, int64) (string, bool, error)
	IssueEmailVerificationToken(context.Context, int64) (string, string, error)
	VerifyEmail(context.Context, int64, string) error
}

func (s *Server) verificationAccount(w http.ResponseWriter, r *http.Request) (auth.Account, bool) {
	cookie, err := r.Cookie(s.cookieName)
	if err == nil {
		account, err := s.auth.Authenticate(r.Context(), cookie.Value)
		if err == nil {
			s.setCookie(w, cookie.Value, int(auth.IdleTimeout.Seconds()))
			return account, true
		}
		if !errors.Is(err, auth.ErrNoSession) {
			log.Print("email verification session check failed")
			http.Error(w, "authentication temporarily unavailable", http.StatusServiceUnavailable)
			return auth.Account{}, false
		}
		s.setCookie(w, "", -1)
	}
	if r.Method == http.MethodGet && !s.recoveryNoGame() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	} else {
		http.Error(w, "sign in required", http.StatusUnauthorized)
	}
	return auth.Account{}, false
}

func (s *Server) verificationPage(w http.ResponseWriter, r *http.Request, account auth.Account, status int, message string) {
	service, ok := s.auth.(verificationService)
	if !ok {
		log.Print("email verification service unavailable")
		http.Error(w, "email verification temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	email, verified, err := service.EmailVerificationStatus(r.Context(), account.ID)
	if err != nil {
		log.Print("email verification status failed")
		http.Error(w, "email verification temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	renderHTML(w, status, htmlPage{Title: "Email verification", Username: account.Username,
		NoGame: s.recoveryNoGame(), VerifyPage: true, Email: email, EmailVerified: verified, Message: message})
}

func (s *Server) handleVerifyEmailPage(w http.ResponseWriter, r *http.Request) {
	account, ok := s.verificationAccount(w, r)
	if ok {
		s.verificationPage(w, r, account, http.StatusOK, "")
	}
}

func (s *Server) verificationResult(w http.ResponseWriter, r *http.Request, account auth.Account, status int, message string) {
	if r.PostForm.Get("_html") == "1" {
		s.verificationPage(w, r, account, status, message)
		return
	}
	w.WriteHeader(status)
	fmt.Fprintln(w, message)
}

func (s *Server) verificationAdmission(w http.ResponseWriter, r *http.Request, accountID int64, ipLimit, accountLimit *authLimiter) bool {
	for _, entry := range []struct {
		limiter *authLimiter
		key     string
	}{{ipLimit, s.clientIP(r)}, {accountLimit, strconv.FormatInt(accountID, 10)}} {
		slot, err := entry.limiter.reserve(r.Context(), entry.key)
		if err != nil {
			rateLimitUnavailable(w, err)
			return false
		}
		if slot == nil {
			http.Error(w, "too many requests; try again later", http.StatusTooManyRequests)
			return false
		}
		if err := entry.limiter.finish(slot, true, false); err != nil {
			rateLimitUnavailable(w, err)
			return false
		}
	}
	return true
}

func (s *Server) handleVerifyEmailRequest(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	account, ok := s.verificationAccount(w, r)
	if !ok {
		return
	}
	select {
	case s.recoverySlots <- struct{}{}:
	default:
		log.Print("email verification delivery capacity exhausted")
		s.verificationResult(w, r, account, http.StatusServiceUnavailable, "Email delivery is busy. Try again.")
		return
	}
	if !s.verificationAdmission(w, r, account.ID, s.verifyRequestIP, s.verifyRequestAccount) {
		<-s.recoverySlots
		return
	}
	service, ok := s.auth.(verificationService)
	if !ok {
		<-s.recoverySlots
		log.Print("email verification service unavailable")
		http.Error(w, "email verification temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	code, address, err := service.IssueEmailVerificationToken(r.Context(), account.ID)
	if err != nil {
		<-s.recoverySlots
		if errors.Is(err, auth.ErrVerificationUnavailable) {
			s.verificationResult(w, r, account, http.StatusBadRequest, "There is no unverified address on this account.")
		} else {
			log.Print("email verification token issuance failed")
			s.verificationResult(w, r, account, http.StatusServiceUnavailable, "Email verification is temporarily unavailable.")
		}
		return
	}
	go func() {
		defer func() { <-s.recoverySlots }()
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if err := s.opts.Recovery.Sender.SendVerification(ctx, address, code, s.opts.Recovery.VerificationURL()); err != nil {
			log.Print("email verification delivery failed")
		}
	}()
	s.verificationResult(w, r, account, http.StatusOK, "We will try to send a verification code. Check your email.")
}

func (s *Server) handleVerifyEmail(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	account, ok := s.verificationAccount(w, r)
	if !ok || !s.verificationAdmission(w, r, account.ID, s.verifySubmitIP, s.verifySubmitAccount) {
		return
	}
	service, ok := s.auth.(verificationService)
	if !ok {
		log.Print("email verification service unavailable")
		http.Error(w, "email verification temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	if len(r.PostForm["code"]) != 1 {
		s.verificationResult(w, r, account, http.StatusBadRequest, "Enter the verification code from your email.")
		return
	}
	err := service.VerifyEmail(r.Context(), account.ID, strings.TrimSpace(r.PostFormValue("code")))
	switch {
	case err == nil:
		s.verificationResult(w, r, account, http.StatusOK, "Your email address is verified for password recovery.")
	case errors.Is(err, auth.ErrInvalidVerificationToken):
		s.verificationResult(w, r, account, http.StatusBadRequest, "This code is invalid or expired. Request a new code.")
	default:
		log.Print("email verification failed")
		s.verificationResult(w, r, account, http.StatusServiceUnavailable, "Verification could not be confirmed. Check the status below before retrying.")
	}
}
