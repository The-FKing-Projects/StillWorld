package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/auth"
)

const maxWebAuthnBodyBytes = 64 << 10

type webAuthnService interface {
	BeginPasskeyRegistration(context.Context, int64, string) ([]byte, error)
	FinishPasskeyRegistration(context.Context, int64, string, []byte, string) error
	BeginPasskeyLogin(context.Context, string) ([]byte, error)
	CompleteLoginPasskey(context.Context, string, []byte) (string, error)
	ListPasskeys(context.Context, int64) ([]auth.PasskeyCredential, error)
	DeletePasskey(context.Context, int64, int64, string) error
	LoginChallengeFactors(context.Context, string) (auth.SecondFactorInfo, error)
}

type webAuthnConfigured interface {
	WebAuthnConfigured() bool
}

func (s *Server) webauthn() webAuthnService {
	svc, ok := s.auth.(webAuthnService)
	if !ok {
		return nil
	}
	// *auth.Service always implements webAuthnService; require an explicit
	// configured RP before advertising or serving passkey UI.
	if c, ok := s.auth.(webAuthnConfigured); ok && !c.WebAuthnConfigured() {
		return nil
	}
	return svc
}

func (s *Server) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	svc := s.webauthn()
	if svc == nil {
		http.Error(w, "webauthn unavailable", http.StatusServiceUnavailable)
		return
	}
	acct, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	options, err := svc.BeginPasskeyRegistration(r.Context(), acct.ID, acct.Username)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, options)
	case errors.Is(err, auth.ErrWebAuthnUnavailable):
		http.Error(w, "webauthn unavailable", http.StatusServiceUnavailable)
	default:
		log.Print("passkey register begin failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	svc := s.webauthn()
	if svc == nil {
		http.Error(w, "webauthn unavailable", http.StatusServiceUnavailable)
		return
	}
	acct, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	body, label, err := readWebAuthnBody(r)
	if err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
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
	err = svc.FinishPasskeyRegistration(r.Context(), acct.ID, acct.Username, body, label)
	failed := errors.Is(err, auth.ErrWebAuthnInvalid) || errors.Is(err, auth.ErrInvalidInput)
	if ipErr := s.loginIP.finish(ipSlot, failed, err == nil); ipErr != nil {
		log.Print("passkey register finish rate limit finalization failed")
		if err != nil {
			rateLimitUnavailable(w, ipErr)
			return
		}
	}
	switch {
	case err == nil:
		if s.htmlForm(r) {
			http.Redirect(w, r, "/passkeys", http.StatusSeeOther)
			return
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintln(w, "passkey registered")
	case errors.Is(err, auth.ErrWebAuthnInvalid), errors.Is(err, auth.ErrInvalidInput):
		http.Error(w, "invalid passkey response", http.StatusBadRequest)
	case errors.Is(err, auth.ErrWebAuthnUnavailable):
		http.Error(w, "webauthn unavailable", http.StatusServiceUnavailable)
	default:
		log.Print("passkey register finish failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	svc := s.webauthn()
	if svc == nil {
		http.Error(w, "webauthn unavailable", http.StatusServiceUnavailable)
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
	rowID, err := strconv.ParseInt(strings.TrimSpace(r.PostFormValue("id")), 10, 64)
	if err != nil || rowID <= 0 {
		http.Error(w, "invalid credential id", http.StatusBadRequest)
		return
	}
	err = svc.DeletePasskey(r.Context(), acct.ID, rowID, r.PostFormValue("password"))
	switch {
	case err == nil:
		if s.htmlForm(r) {
			http.Redirect(w, r, "/passkeys", http.StatusSeeOther)
			return
		}
		fmt.Fprintln(w, "passkey deleted")
	case errors.Is(err, auth.ErrInvalidCredentials):
		if s.htmlForm(r) {
			s.renderPasskeysPage(w, r, acct, "invalid password", http.StatusUnauthorized, strconv.FormatInt(rowID, 10))
			return
		}
		http.Error(w, "invalid password", http.StatusUnauthorized)
	case errors.Is(err, auth.ErrWebAuthnCredentialMissing):
		if s.htmlForm(r) {
			s.renderPasskeysPage(w, r, acct, "passkey not found", http.StatusNotFound, strconv.FormatInt(rowID, 10))
			return
		}
		http.Error(w, "passkey not found", http.StatusNotFound)
	default:
		log.Print("passkey delete failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) handleLoginPasskeyBegin(w http.ResponseWriter, r *http.Request) {
	svc := s.webauthn()
	if svc == nil {
		http.Error(w, "webauthn unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	options, err := svc.BeginPasskeyLogin(r.Context(), s.challengeToken(r))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, options)
	case errors.Is(err, auth.ErrTOTPChallenge):
		s.setChallengeCookie(w, "", -1)
		http.Error(w, "sign-in challenge expired; sign in again", http.StatusUnauthorized)
	case errors.Is(err, auth.ErrWebAuthnNotEnabled):
		http.Error(w, "passkey not enabled", http.StatusConflict)
	case errors.Is(err, auth.ErrWebAuthnUnavailable):
		http.Error(w, "webauthn unavailable", http.StatusServiceUnavailable)
	default:
		log.Print("passkey login begin failed")
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) handleLoginPasskey(w http.ResponseWriter, r *http.Request) {
	svc := s.webauthn()
	if svc == nil {
		http.Error(w, "webauthn unavailable", http.StatusServiceUnavailable)
		return
	}
	body, _, err := readWebAuthnBody(r)
	if err != nil {
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
	token, err := svc.CompleteLoginPasskey(r.Context(), challenge, body)
	failed := errors.Is(err, auth.ErrWebAuthnInvalid) || errors.Is(err, auth.ErrTOTPChallenge) ||
		errors.Is(err, auth.ErrWebAuthnNotEnabled) || errors.Is(err, auth.ErrWebAuthnUnavailable)
	if ipErr := s.loginIP.finish(ipSlot, failed, err == nil); ipErr != nil {
		log.Print("passkey login rate limit finalization failed")
		if err != nil {
			rateLimitUnavailable(w, ipErr)
			return
		}
	}
	switch {
	case err == nil:
		s.setChallengeCookie(w, "", -1)
		s.setCookie(w, token, int(auth.IdleTimeout/time.Second))
		if wantsHTMLRedirect(r) {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		fmt.Fprintln(w, "logged in")
	case errors.Is(err, auth.ErrTOTPChallenge):
		s.setChallengeCookie(w, "", -1)
		s.totpLoginError(w, r, "sign-in challenge expired; sign in again", http.StatusUnauthorized)
	case errors.Is(err, auth.ErrWebAuthnInvalid), errors.Is(err, auth.ErrWebAuthnNotEnabled):
		s.totpLoginError(w, r, "invalid passkey response", http.StatusUnauthorized)
	case errors.Is(err, auth.ErrWebAuthnUnavailable):
		http.Error(w, "webauthn unavailable", http.StatusServiceUnavailable)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) handleHTMLPasskeys(w http.ResponseWriter, r *http.Request) {
	if s.webauthn() == nil {
		renderHTML(w, http.StatusServiceUnavailable, htmlPage{
			Title: "Passkeys unavailable", Message: "Passkeys are not configured on this server.",
		})
		return
	}
	account, ok := s.htmlAccount(w, r)
	if !ok {
		return
	}
	s.renderPasskeysPage(w, r, account, "", http.StatusOK, "")
}

func (s *Server) renderPasskeysPage(w http.ResponseWriter, r *http.Request, account auth.Account, message string, status int, deleteID string) {
	creds, err := s.webauthn().ListPasskeys(r.Context(), account.ID)
	if err != nil {
		log.Print("list passkeys failed")
		renderHTML(w, http.StatusServiceUnavailable, htmlPage{
			Title: "Temporarily unavailable", Message: "Passkeys could not be loaded. Try again.",
		})
		return
	}
	var b strings.Builder
	if message != "" {
		b.WriteString(message)
		if len(creds) > 0 {
			b.WriteString(" ")
		}
	}
	if len(creds) == 0 {
		if message == "" {
			b.WriteString("No passkeys registered yet.")
		}
	} else {
		b.WriteString("Registered passkeys:")
		for _, c := range creds {
			label := c.Label
			if label == "" {
				label = fmt.Sprintf("passkey #%d", c.ID)
			}
			fmt.Fprintf(&b, " [%d] %s", c.ID, label)
		}
	}
	renderHTML(w, status, htmlPage{
		Title: "Passkeys", Username: account.Username,
		Description: "Optional passkeys as a second factor after your password. Registration uses a short browser ceremony; game actions stay text-first.",
		Message:     b.String(), CanPasskey: true, PasskeyPage: true,
		PasskeyCeremony: true, PasskeyDeleteID: deleteID,
	})
}

func readWebAuthnBody(r *http.Request) (body []byte, label string, err error) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		defer r.Body.Close()
		body, err = io.ReadAll(io.LimitReader(r.Body, maxWebAuthnBodyBytes+1))
		if err != nil {
			return nil, "", err
		}
		if len(body) > maxWebAuthnBodyBytes {
			return nil, "", errors.New("body too large")
		}
		return body, "", nil
	}
	if err := r.ParseForm(); err != nil {
		return nil, "", err
	}
	raw := r.PostFormValue("credential")
	if raw == "" {
		return nil, "", errors.New("missing credential")
	}
	return []byte(raw), r.PostFormValue("label"), nil
}

func writeJSON(w http.ResponseWriter, status int, raw []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func wantsHTMLRedirect(r *http.Request) bool {
	if r.URL.Query().Get("_html") == "1" {
		return true
	}
	accept := r.Header.Get("Accept")
	return accept != "" && strings.Contains(accept, "text/html") &&
		!strings.Contains(accept, "application/json")
}
