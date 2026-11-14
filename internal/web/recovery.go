package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/auth"
)

type recoveryService interface {
	IssueResetTokenForDelivery(context.Context, string) (string, string, error)
	RedeemResetToken(context.Context, string, string) error
}

const recoveryRequestMessage = "If the address belongs to an eligible account, we will try to send a reset code."

func (s *Server) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	ipSlot, err := s.recoveryIP.reserve(r.Context(), s.clientIP(r))
	if err != nil {
		rateLimitUnavailable(w, err)
		return
	}
	if ipSlot == nil {
		http.Error(w, "too many requests; try again later", http.StatusTooManyRequests)
		return
	}
	if err := s.recoveryIP.finish(ipSlot, true, false); err != nil {
		rateLimitUnavailable(w, err)
		return
	}
	email := r.PostFormValue("email")
	if len(r.PostForm["email"]) != 1 {
		email = ""
	}
	select {
	case s.recoverySlots <- struct{}{}:
		emailSlot, err := s.recoveryEmail.reserve(r.Context(), strings.ToLower(email))
		if err != nil {
			<-s.recoverySlots
			rateLimitUnavailable(w, err)
			return
		}
		if emailSlot != nil {
			if err := s.recoveryEmail.finish(emailSlot, true, false); err != nil {
				<-s.recoverySlots
				rateLimitUnavailable(w, err)
				return
			}
			go s.deliverReset(email)
		} else {
			<-s.recoverySlots
		}
	default:
		log.Print("password recovery delivery capacity exhausted")
	}
	if r.PostForm.Get("_html") == "1" {
		renderHTML(w, http.StatusOK, htmlPage{Title: "Check your email", Message: recoveryRequestMessage, Recovery: true, NoGame: s.recoveryNoGame()})
		return
	}
	fmt.Fprintln(w, recoveryRequestMessage)
}

func (s *Server) deliverReset(email string) {
	defer func() { <-s.recoverySlots }()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	service, ok := s.auth.(recoveryService)
	if !ok {
		log.Print("password recovery service is unavailable")
		return
	}
	code, address, err := service.IssueResetTokenForDelivery(ctx, email)
	if err == auth.ErrRecoveryUnavailable {
		return
	}
	if err != nil {
		log.Print("password recovery token issuance failed")
		return
	}
	if err := s.opts.Recovery.Sender.SendReset(ctx, address, code, s.opts.Recovery.ResetURL()); err != nil {
		log.Print("password recovery delivery failed")
	}
}

func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	ipSlot, err := s.resetIP.reserve(r.Context(), s.clientIP(r))
	if err != nil {
		rateLimitUnavailable(w, err)
		return
	}
	if ipSlot == nil {
		http.Error(w, "too many requests; try again later", http.StatusTooManyRequests)
		return
	}
	if err := s.resetIP.finish(ipSlot, true, false); err != nil {
		rateLimitUnavailable(w, err)
		return
	}
	if len(r.PostForm["code"]) != 1 || len(r.PostForm["password"]) != 1 ||
		len(r.PostForm["confirm_password"]) != 1 || r.PostFormValue("password") != r.PostFormValue("confirm_password") {
		s.resetResult(w, r, http.StatusBadRequest, "Check the code and matching passwords, then try again.", true)
		return
	}
	if !s.acquire(r.Context()) {
		s.resetResult(w, r, http.StatusServiceUnavailable, "The server is busy. Try again.", true)
		return
	}
	defer s.release()
	service, ok := s.auth.(recoveryService)
	if !ok {
		log.Print("password recovery service is unavailable")
		s.resetResult(w, r, http.StatusServiceUnavailable, "Password recovery is temporarily unavailable.", true)
		return
	}
	err = service.RedeemResetToken(r.Context(), strings.TrimSpace(r.PostFormValue("code")), r.PostFormValue("password"))
	switch {
	case err == nil:
		s.resetResult(w, r, http.StatusOK, "Your password has been changed. Sign in with your new password.", false)
	case errors.Is(err, auth.ErrInvalidResetToken):
		s.resetResult(w, r, http.StatusBadRequest, "This code is invalid or expired. Request a new code.", true)
	case errors.Is(err, auth.ErrInvalidInput):
		s.resetResult(w, r, http.StatusBadRequest, "Choose a password with at least 8 characters and at most 256 bytes.", true)
	case errors.Is(err, auth.ErrTokenResetUnknown):
		log.Print("password recovery outcome is unknown")
		s.resetResult(w, r, http.StatusServiceUnavailable, "The change could not be confirmed. Try signing in with the new password, or request a new code.", false)
	default:
		log.Print("password recovery reset failed")
		s.resetResult(w, r, http.StatusServiceUnavailable, "Password recovery is temporarily unavailable. Try again.", true)
	}
}

func (s *Server) resetResult(w http.ResponseWriter, r *http.Request, status int, message string, showForm bool) {
	if r.PostForm.Get("_html") == "1" {
		page := htmlPage{Title: "Reset password", Message: message, Recovery: true, NoGame: s.recoveryNoGame(), ResetDone: !showForm}
		if showForm {
			page.AuthKind = "reset-password"
		}

		renderHTML(w, status, page)
		return
	}
	w.WriteHeader(status)
	fmt.Fprintln(w, message)
}

func (s *Server) recoveryNoGame() bool {
	return s.opts.Presentation == nil || s.actions == nil
}
