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

type pendingSignupService interface {
	StartPendingSignup(context.Context, string, string, string) (string, string, error)
	CompletePendingSignup(context.Context, string) (int64, error)
}

const deferredSignupMessage = "If this address can be used, we will send a code to complete signup. Check your email."

func (s *Server) handleDeferredSignup(w http.ResponseWriter, r *http.Request, email string) {
	service, ok := s.auth.(pendingSignupService)
	if !ok {
		log.Print("deferred signup service unavailable")
		http.Error(w, "signup temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	// StartPendingSignup hashes with Argon2id; share the login/signup hash budget.
	if !s.acquire(r.Context()) {
		http.Error(w, "server busy", http.StatusServiceUnavailable)
		return
	}
	defer s.release()

	ipSlot, err := s.signupIP.reserve(r.Context(), s.clientIP(r))
	if err != nil {
		rateLimitUnavailable(w, err)
		return
	}
	if ipSlot == nil {
		http.Error(w, "too many signups; try again later", http.StatusTooManyRequests)
		return
	}
	if err := s.signupIP.finish(ipSlot, true, false); err != nil {
		rateLimitUnavailable(w, err)
		return
	}

	select {
	case s.recoverySlots <- struct{}{}:
	default:
		log.Print("signup delivery capacity exhausted")
		s.deferredSignupResult(w, r, http.StatusServiceUnavailable, "Email delivery is busy. Try again.")
		return
	}
	emailSlot, err := s.signupEmail.reserve(r.Context(), strings.ToLower(email))
	if err != nil {
		<-s.recoverySlots
		rateLimitUnavailable(w, err)
		return
	}
	if emailSlot == nil {
		// Same generic success as a delivered signup so quota exhaustion does
		// not reveal whether the address is registered or recently contacted.
		<-s.recoverySlots
		s.deferredSignupResult(w, r, http.StatusOK, deferredSignupMessage)
		return
	}
	// Hold the reservation as pending until StartPendingSignup succeeds so
	// invalid input or username conflicts do not burn the delivery quota.
	releaseEmail := func(charge bool) bool {
		if err := s.signupEmail.finish(emailSlot, charge, false); err != nil {
			<-s.recoverySlots
			rateLimitUnavailable(w, err)
			return false
		}
		return true
	}
	code, address, err := service.StartPendingSignup(r.Context(), r.PostFormValue("username"), email, r.PostFormValue("password"))
	switch {
	case err == nil:
		if !releaseEmail(true) {
			return
		}
		go func() {
			defer func() { <-s.recoverySlots }()
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			if err := s.opts.Recovery.Sender.SendSignup(ctx, address, code, s.opts.Recovery.SignupURL()); err != nil {
				log.Print("signup delivery failed")
			}
		}()
		s.deferredSignupResult(w, r, http.StatusOK, deferredSignupMessage)
	case errors.Is(err, auth.ErrPendingSignupUnavailable):
		if !releaseEmail(false) {
			return
		}
		<-s.recoverySlots
		s.deferredSignupResult(w, r, http.StatusOK, deferredSignupMessage)
	case errors.Is(err, auth.ErrInvalidInput):
		if !releaseEmail(false) {
			return
		}
		<-s.recoverySlots
		s.authError(w, r, "signup", strings.TrimPrefix(err.Error(), auth.ErrInvalidInput.Error()+": "), http.StatusBadRequest)
	case errors.Is(err, auth.ErrUsernameTaken):
		if !releaseEmail(false) {
			return
		}
		<-s.recoverySlots
		s.authError(w, r, "signup", "username already taken", http.StatusConflict)
	default:
		if !releaseEmail(false) {
			return
		}
		<-s.recoverySlots
		log.Print("deferred signup failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) deferredSignupResult(w http.ResponseWriter, r *http.Request, status int, message string) {
	if s.htmlForm(r) {
		renderHTML(w, status, htmlPage{
			Title: "Check your email", Description: message, AuthKind: "complete-signup",
			Recovery: true, NoGame: s.recoveryNoGame(),
		})
		return
	}
	w.WriteHeader(status)
	fmt.Fprintln(w, message)
}

func (s *Server) handleCompleteSignupPage(w http.ResponseWriter, _ *http.Request) {
	renderHTML(w, http.StatusOK, htmlPage{
		Title: "Complete signup", Description: "Enter the code from your email to finish creating your account.",
		AuthKind: "complete-signup", Recovery: true, NoGame: s.recoveryNoGame(),
	})
}

func (s *Server) handleCompleteSignup(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	// Completion inserts a pre-hashed pending row; do not take Argon2 hash slots.
	slot, err := s.completeSignupIP.reserve(r.Context(), s.clientIP(r))
	if err != nil {
		rateLimitUnavailable(w, err)
		return
	}
	if slot == nil {
		http.Error(w, "too many requests; try again later", http.StatusTooManyRequests)
		return
	}
	if err := s.completeSignupIP.finish(slot, true, false); err != nil {
		rateLimitUnavailable(w, err)
		return
	}
	service, ok := s.auth.(pendingSignupService)
	if !ok {
		log.Print("deferred signup service unavailable")
		http.Error(w, "signup temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	if len(r.PostForm["code"]) != 1 {
		s.completeSignupResult(w, r, http.StatusBadRequest, "Enter the signup code from your email.")
		return
	}
	_, err = service.CompletePendingSignup(r.Context(), strings.TrimSpace(r.PostFormValue("code")))
	switch {
	case err == nil:
		if s.htmlForm(r) {
			renderHTML(w, http.StatusOK, htmlPage{
				Title: "Account created", Message: "Your email is verified. Sign in with your username and password.",
				Recovery: true, NoGame: s.recoveryNoGame(),
			})
			return
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintln(w, "account created")
	case errors.Is(err, auth.ErrInvalidPendingSignup), errors.Is(err, auth.ErrPendingSignupUnavailable):
		s.completeSignupResult(w, r, http.StatusBadRequest, "This code is invalid or expired. Request a new signup email.")
	case errors.Is(err, auth.ErrUsernameTaken):
		s.completeSignupResult(w, r, http.StatusConflict, "That username was taken before the code was used. Start signup again with a different username.")
	default:
		log.Print("complete signup failed")
		s.completeSignupResult(w, r, http.StatusServiceUnavailable, "Signup could not be confirmed. Check whether you can sign in before retrying.")
	}
}

func (s *Server) completeSignupResult(w http.ResponseWriter, r *http.Request, status int, message string) {
	if s.htmlForm(r) {
		renderHTML(w, status, htmlPage{
			Title: "Complete signup", Description: "Enter the code from your email to finish creating your account.",
			AuthKind: "complete-signup", Message: message, Recovery: true, NoGame: s.recoveryNoGame(),
		})
		return
	}
	http.Error(w, message, status)
}
