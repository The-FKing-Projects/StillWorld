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

type totpService interface {
	BeginTOTPEnrolment(context.Context, int64, string) (auth.TOTPEnrolment, error)
	ConfirmTOTPEnrolment(context.Context, int64, string) ([]string, error)
	DisableTOTP(context.Context, int64, string) error
	CompleteLoginTOTP(context.Context, string, string) (string, error)
	CompleteLoginRecoveryCode(context.Context, string, string) (string, error)
}

func (s *Server) totp() totpService {
	svc, _ := s.auth.(totpService)
	return svc
}

func (s *Server) challengeCookieName() string {
	if s.opts.InsecureCookies {
		return "login-challenge"
	}
	return "__Host-login-challenge"
}

func (s *Server) setChallengeCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.challengeCookieName(),
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   !s.opts.InsecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) challengeToken(r *http.Request) string {
	if v := strings.TrimSpace(r.PostFormValue("challenge")); v != "" {
		return v
	}
	if c, err := r.Cookie(s.challengeCookieName()); err == nil {
		return c.Value
	}
	return ""
}

func (s *Server) handleLoginTOTP(w http.ResponseWriter, r *http.Request) {
	s.finishLoginSecondFactor(w, r, false)
}

func (s *Server) handleLoginRecoveryCode(w http.ResponseWriter, r *http.Request) {
	s.finishLoginSecondFactor(w, r, true)
}

func (s *Server) finishLoginSecondFactor(w http.ResponseWriter, r *http.Request, recovery bool) {
	svc := s.totp()
	if svc == nil {
		http.Error(w, "totp unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	ip := s.clientIP(r)
	ipSlot, err := s.loginIP.reserve(r.Context(), ip)
	if err != nil {
		rateLimitUnavailable(w, err)
		return
	}
	if ipSlot == nil {
		http.Error(w, "too many failed attempts; try again later", http.StatusTooManyRequests)
		return
	}
	challenge := s.challengeToken(r)
	code := r.PostFormValue("code")
	var token string
	if recovery {
		token, err = svc.CompleteLoginRecoveryCode(r.Context(), challenge, code)
	} else {
		token, err = svc.CompleteLoginTOTP(r.Context(), challenge, code)
	}
	failed := errors.Is(err, auth.ErrTOTPInvalid) || errors.Is(err, auth.ErrTOTPChallenge) ||
		errors.Is(err, auth.ErrTOTPNotEnabled) || errors.Is(err, auth.ErrTOTPUnavailable)
	if ipErr := s.loginIP.finish(ipSlot, failed, err == nil); ipErr != nil {
		// Session may already exist; do not convert a successful login into 503.
		log.Print("totp login rate limit finalization failed")
		if err != nil {
			rateLimitUnavailable(w, ipErr)
			return
		}
	}
	switch {
	case err == nil:
		s.setChallengeCookie(w, "", -1)
		s.setCookie(w, token, int(auth.IdleTimeout/time.Second))
		if s.htmlForm(r) {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		fmt.Fprintln(w, "logged in")
	case errors.Is(err, auth.ErrTOTPChallenge):
		s.setChallengeCookie(w, "", -1)
		s.totpLoginError(w, r, "sign-in challenge expired; sign in again", http.StatusUnauthorized)
	case errors.Is(err, auth.ErrTOTPInvalid), errors.Is(err, auth.ErrTOTPNotEnabled):
		s.totpLoginError(w, r, "invalid authentication code", http.StatusUnauthorized)
	case errors.Is(err, auth.ErrTOTPUnavailable):
		http.Error(w, "totp unavailable", http.StatusServiceUnavailable)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) totpLoginError(w http.ResponseWriter, r *http.Request, message string, status int) {
	if !s.htmlForm(r) {
		http.Error(w, message, status)
		return
	}
	renderHTML(w, status, htmlPage{
		Title: "Two-factor authentication", AuthKind: "login/totp", Message: message,
	})
}

func (s *Server) handleHTMLLoginTOTP(w http.ResponseWriter, r *http.Request) {
	if _, err := r.Cookie(s.challengeCookieName()); err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	info := auth.SecondFactorInfo{TOTP: s.totp() != nil}
	if svc := s.webauthn(); svc != nil {
		if c, err := r.Cookie(s.challengeCookieName()); err == nil {
			if factors, ferr := svc.LoginChallengeFactors(r.Context(), c.Value); ferr == nil {
				info = factors
			}
		}
	}
	desc := "Complete sign-in with a second factor."
	switch {
	case info.TOTP && info.Passkey:
		desc = "Enter an authenticator code or recovery code, or use a registered passkey."
	case info.Passkey && !info.TOTP:
		desc = "Use a registered passkey to finish sign-in."
	case info.TOTP:
		desc = "Enter the code from your authenticator app, or a one-time recovery code."
	}
	renderHTML(w, http.StatusOK, htmlPage{
		Title: "Two-factor authentication", Description: desc,
		AuthKind: "login/totp", CanPasskey: info.Passkey, CanTOTP: info.TOTP,
	})
}

func (s *Server) requireAccount(w http.ResponseWriter, r *http.Request) (auth.Account, bool) {
	c, err := r.Cookie(s.cookieName)
	if err != nil {
		http.Error(w, "not logged in", http.StatusUnauthorized)
		return auth.Account{}, false
	}
	acct, err := s.auth.Authenticate(r.Context(), c.Value)
	if errors.Is(err, auth.ErrNoSession) {
		s.setCookie(w, "", -1)
		http.Error(w, "not logged in", http.StatusUnauthorized)
		return auth.Account{}, false
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return auth.Account{}, false
	}
	return acct, true
}

func (s *Server) handleTOTPBegin(w http.ResponseWriter, r *http.Request) {
	svc := s.totp()
	if svc == nil {
		http.Error(w, "totp unavailable", http.StatusServiceUnavailable)
		return
	}
	acct, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	enrolment, err := svc.BeginTOTPEnrolment(r.Context(), acct.ID, acct.Username)
	switch {
	case err == nil:
		if s.htmlForm(r) {
			renderHTML(w, http.StatusOK, htmlPage{
				Title: "Enable authenticator", AuthKind: "totp/confirm",
				Description: "Add this secret to your authenticator app, then enter a code to confirm.",
				Message:     "Secret: " + enrolment.Secret,
				TOTPURL:     enrolment.URL,
			})
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "secret %s\nurl %s\n", enrolment.Secret, enrolment.URL)
	case errors.Is(err, auth.ErrTOTPAlreadyEnabled):
		http.Error(w, "totp already enabled", http.StatusConflict)
	case errors.Is(err, auth.ErrTOTPUnavailable):
		http.Error(w, "totp unavailable", http.StatusServiceUnavailable)
	default:
		log.Print("totp begin failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	svc := s.totp()
	if svc == nil {
		http.Error(w, "totp unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	acct, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	ipSlot, err := s.loginIP.reserve(r.Context(), s.clientIP(r))
	if err != nil {
		rateLimitUnavailable(w, err)
		return
	}
	if ipSlot == nil {
		http.Error(w, "too many failed attempts; try again later", http.StatusTooManyRequests)
		return
	}
	codes, err := svc.ConfirmTOTPEnrolment(r.Context(), acct.ID, r.PostFormValue("code"))
	failed := errors.Is(err, auth.ErrTOTPInvalid)
	if ipErr := s.loginIP.finish(ipSlot, failed, err == nil); ipErr != nil {
		// Enrolment already committed; never discard newly issued recovery codes
		// or convert a successful confirm into 503 because limiter release failed.
		log.Print("totp confirm rate limit finalization failed")
		if err != nil {
			rateLimitUnavailable(w, ipErr)
			return
		}
	}
	switch {
	case err == nil:
		if s.htmlForm(r) {
			renderHTML(w, http.StatusOK, htmlPage{
				Title:   "Authenticator enabled",
				Message: "Store these recovery codes; each works once: " + strings.Join(codes, ", "),
			})
			return
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintln(w, "totp enabled")
		for _, c := range codes {
			fmt.Fprintln(w, c)
		}
	case errors.Is(err, auth.ErrTOTPInvalid):
		s.authError(w, r, "totp/confirm", "invalid authentication code", http.StatusBadRequest)
	case errors.Is(err, auth.ErrTOTPUnavailable):
		http.Error(w, "totp unavailable", http.StatusServiceUnavailable)
	default:
		log.Print("totp confirm failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	svc := s.totp()
	if svc == nil {
		http.Error(w, "totp unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if !s.acquire(r.Context()) {
		http.Error(w, "server busy", http.StatusServiceUnavailable)
		return
	}
	defer s.release()
	acct, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	err := svc.DisableTOTP(r.Context(), acct.ID, r.PostFormValue("password"))
	switch {
	case err == nil:
		if s.htmlForm(r) {
			renderHTML(w, http.StatusOK, htmlPage{Title: "Authenticator disabled", Message: "Two-factor authentication is off for this account."})
			return
		}
		fmt.Fprintln(w, "totp disabled")
	case errors.Is(err, auth.ErrInvalidCredentials):
		s.authError(w, r, "totp/disable", "invalid password", http.StatusUnauthorized)
	case errors.Is(err, auth.ErrTOTPNotEnabled):
		http.Error(w, "totp not enabled", http.StatusConflict)
	default:
		log.Print("totp disable failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) handleHTMLTOTP(w http.ResponseWriter, r *http.Request) {
	if s.totp() == nil {
		renderHTML(w, http.StatusServiceUnavailable, htmlPage{
			Title: "Authenticator unavailable", Message: "Two-factor authentication is not configured on this server.",
		})
		return
	}
	account, ok := s.htmlAccount(w, r)
	if !ok {
		return
	}
	renderHTML(w, http.StatusOK, htmlPage{
		Title: "Authenticator app", Username: account.Username,
		Description: "Optional TOTP two-factor authentication. Enabling requires a confirmation code from your authenticator.",
		AuthKind:    "totp/begin",
	})
}
