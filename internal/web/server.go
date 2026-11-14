// Package web is StillWorld's HTTP layer: signup, login, logout, and sessions
// carried in a cookie.
package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/actiondata"
	"github.com/The-FKing-Projects/StillWorld/internal/actionengine"
	"github.com/The-FKing-Projects/StillWorld/internal/auth"
	"github.com/The-FKing-Projects/StillWorld/presentation"
	"github.com/The-FKing-Projects/StillWorld/recovery"
)

// Authenticator is the account and session behaviour the HTTP layer needs.
type Authenticator interface {
	Register(ctx context.Context, username, email, password string) (int64, error)
	Login(ctx context.Context, username, password string) (string, error)
	Authenticate(ctx context.Context, token string) (auth.Account, error)
	Logout(ctx context.Context, token string) error
}

// ActionService issues request IDs and executes actions for authenticated players.
type ActionService interface {
	IssueRequestID(context.Context, int64, string) (actionengine.RequestID, error)
	Execute(context.Context, int64, string, string, []byte) (actionengine.Outcome, error)
}

// Options configure a Server.
type Options struct {
	// InsecureCookies drops the Secure cookie flag for local development.
	InsecureCookies bool
	// BehindProxy trusts the rightmost X-Forwarded-For entry as the client IP.
	BehindProxy bool
	// Now is the clock; defaults to time.Now.
	Now func() time.Time
	// Actions enables the authenticated action API.
	Actions ActionService
	// Presentation enables the optional server-rendered game interface.
	Presentation presentation.Game
	// Recovery enables optional password recovery.
	Recovery *recovery.Config
	// SharedRateLimits stores authentication admission limits in PostgreSQL.
	SharedRateLimits *sql.DB
}

const (
	maxBodyBytes       int64 = 8 << 10
	maxActionBodyBytes int64 = actiondata.MaxInputBytes
	// maxConcurrentHashes bounds simultaneous Argon2id work, since each hash
	// uses tens of MiB of memory.
	maxConcurrentHashes  = 4
	maxConcurrentActions = 64
	maxActionsPerIP      = 120
	maxActionsPerPlayer  = 60

	loginWindow     = 15 * time.Minute
	maxLoginFailsIP = 20
	maxLoginFailsID = 10
	signupWindow    = time.Hour
	maxSignupsIP    = 5
)

// Server serves the HTTP API.
type Server struct {
	auth          Authenticator
	opts          Options
	actions       ActionService
	hashSlots     chan struct{}
	loginIP       *authLimiter
	loginUser     *authLimiter
	signupIP      *authLimiter
	actionIP      *limiter
	actionUser    *limiter
	actionSlots   chan struct{}
	recoveryIP    *authLimiter
	recoveryEmail *authLimiter
	resetIP       *authLimiter
	recoverySlots chan struct{}
	cookieName    string
}

// New returns a Server.
func New(a Authenticator, opts Options) *Server {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	name := "__Host-session"
	if opts.InsecureCookies {
		// The __Host- prefix requires the Secure attribute.
		name = "session"
	}
	return &Server{
		auth:          a,
		opts:          opts,
		hashSlots:     make(chan struct{}, maxConcurrentHashes),
		loginIP:       newAuthLimiter(opts.SharedRateLimits, limitLoginIP, maxLoginFailsIP, loginWindow, opts.Now),
		loginUser:     newAuthLimiter(opts.SharedRateLimits, limitLoginUser, maxLoginFailsID, loginWindow, opts.Now),
		signupIP:      newAuthLimiter(opts.SharedRateLimits, limitSignupIP, maxSignupsIP, signupWindow, opts.Now),
		actionIP:      newLimiter(maxActionsPerIP, time.Minute, opts.Now),
		actionUser:    newLimiter(maxActionsPerPlayer, time.Minute, opts.Now),
		actionSlots:   make(chan struct{}, maxConcurrentActions),
		recoveryIP:    newAuthLimiter(opts.SharedRateLimits, limitRecoveryIP, 10, time.Hour, opts.Now),
		recoveryEmail: newAuthLimiter(opts.SharedRateLimits, limitRecoveryEmail, 1, 10*time.Minute, opts.Now),
		resetIP:       newAuthLimiter(opts.SharedRateLimits, limitResetIP, 20, 15*time.Minute, opts.Now),
		recoverySlots: make(chan struct{}, 16),
		actions:       opts.Actions,
		cookieName:    name,
	}
}

// Handler returns the routes wrapped in the common protections.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("POST /signup", s.handleSignup)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /me", s.handleMe)
	if s.opts.Recovery != nil {
		mux.HandleFunc("POST /forgot-password", s.handleForgotPassword)
		mux.HandleFunc("POST /reset-password", s.handleResetPassword)
		mux.HandleFunc("GET /forgot-password", s.handleHTMLForgotPassword)
		mux.HandleFunc("GET /reset-password", s.handleHTMLResetPassword)
	}
	if s.actions != nil {
		mux.HandleFunc("POST /api/action-requests/{actionName}", s.handleIssueActionRequest)
		mux.HandleFunc("POST /api/actions/{actionName}", s.handleAction)
	}
	if s.opts.Presentation != nil && s.actions != nil {
		mux.HandleFunc("GET /{$}", s.handleHTMLHome)
		mux.HandleFunc("GET /login", s.handleHTMLLogin)
		mux.HandleFunc("GET /signup", s.handleHTMLSignup)
		mux.HandleFunc("POST /actions/{actionName}", s.handleHTMLAction)
		mux.HandleFunc("GET /actions/results/{nonce}", s.handleHTMLResult)
	}
	return s.protect(mux)
}

// protect adds response headers, a body size limit, and cross-site request
// rejection for state-changing methods.
func (s *Server) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/plain; charset=utf-8")
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")

		if !isSafeMethod(r.Method) {
			if !s.sameOrigin(r) {
				if strings.HasPrefix(r.URL.Path, "/api/actions/") ||
					strings.HasPrefix(r.URL.Path, "/api/action-requests/") {
					writeActionError(w, http.StatusForbidden, "csrf_rejected", "Cross-site requests are not allowed.", "")
					return
				}
				http.Error(w, "cross-site request rejected", http.StatusForbidden)
				return
			}
			bodyLimit := maxBodyBytes
			if strings.HasPrefix(r.URL.Path, "/api/actions/") ||
				strings.HasPrefix(r.URL.Path, "/api/action-requests/") {
				bodyLimit = maxActionBodyBytes
			}
			r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
		}
		next.ServeHTTP(w, r)
	})
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// sameOrigin checks the public scheme without trusting proxy-provided headers.
func (s *Server) sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return r.Header.Get("Sec-Fetch-Site") == "same-origin" || r.Header.Get("X-Stillworld-Request") == "1"
	}
	u, err := url.Parse(origin)
	scheme := "https"
	if s.opts.InsecureCookies {
		scheme = "http"
	}
	return err == nil && u.Scheme == scheme && strings.EqualFold(u.Host, r.Host) &&
		u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if !s.acquire(r.Context()) {
		http.Error(w, "server busy", http.StatusServiceUnavailable)
		return
	}
	defer s.release()

	slot, err := s.signupIP.reserve(r.Context(), ip)
	if err != nil {
		rateLimitUnavailable(w, err)
		return
	}
	if slot == nil {
		http.Error(w, "too many signups; try again later", http.StatusTooManyRequests)
		return
	}
	if err := s.signupIP.finish(slot, true, false); err != nil {
		rateLimitUnavailable(w, err)
		return
	}
	_, err = s.auth.Register(r.Context(), r.PostFormValue("username"), r.PostFormValue("email"), r.PostFormValue("password"))
	switch {
	case err == nil:
		if s.htmlForm(r) {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintln(w, "account created")
	case errors.Is(err, auth.ErrInvalidInput):
		s.authError(w, r, "signup", strings.TrimPrefix(err.Error(), auth.ErrInvalidInput.Error()+": "), http.StatusBadRequest)
	case errors.Is(err, auth.ErrUsernameTaken):
		s.authError(w, r, "signup", "username already taken", http.StatusConflict)
	case errors.Is(err, auth.ErrEmailTaken):
		s.authError(w, r, "signup", "email already in use", http.StatusConflict)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	ip := s.clientIP(r)
	username := strings.ToLower(r.PostFormValue("username"))
	if !s.acquire(r.Context()) {
		http.Error(w, "server busy", http.StatusServiceUnavailable)
		return
	}
	defer s.release()
	ipSlot, err := s.loginIP.reserve(r.Context(), ip)
	if err != nil {
		rateLimitUnavailable(w, err)
		return
	}
	if ipSlot == nil {
		http.Error(w, "too many failed attempts; try again later", http.StatusTooManyRequests)
		return
	}
	userSlot, userErr := s.loginUser.reserve(r.Context(), username)
	if userErr != nil {
		if err := s.loginIP.finish(ipSlot, false, false); err != nil {
			log.Print("authentication rate limit release failed")
		}
		rateLimitUnavailable(w, userErr)
		return
	}
	if userSlot == nil {
		if err := s.loginIP.finish(ipSlot, false, false); err != nil {
			rateLimitUnavailable(w, err)
			return
		}
		http.Error(w, "too many failed attempts; try again later", http.StatusTooManyRequests)
		return
	}

	token, err := s.auth.Login(r.Context(), r.PostFormValue("username"), r.PostFormValue("password"))
	failed := errors.Is(err, auth.ErrInvalidCredentials)
	ipErr := s.loginIP.finish(ipSlot, failed, false)
	userErr = s.loginUser.finish(userSlot, failed, err == nil)
	if ipErr != nil || userErr != nil {
		rateLimitUnavailable(w, errors.Join(ipErr, userErr))
		return
	}
	switch {
	case err == nil:
		s.setCookie(w, token, int(auth.IdleTimeout/time.Second))
		if s.htmlForm(r) {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		fmt.Fprintln(w, "logged in")
	case errors.Is(err, auth.ErrInvalidCredentials):
		s.authError(w, r, "login", "invalid username or password", http.StatusUnauthorized)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func rateLimitUnavailable(w http.ResponseWriter, err error) {
	if errors.Is(err, errLimitCapacity) {
		log.Print("authentication rate limit capacity exhausted")
	} else {
		log.Print("authentication rate limit unavailable")
	}
	http.Error(w, "authentication temporarily unavailable; try again", http.StatusServiceUnavailable)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.cookieName); err == nil {
		if err := s.auth.Logout(r.Context(), c.Value); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	s.setCookie(w, "", -1)
	if s.opts.Presentation != nil && r.FormValue("_html") == "1" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	fmt.Fprintln(w, "logged out")
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(s.cookieName)
	if err != nil {
		http.Error(w, "not logged in", http.StatusUnauthorized)
		return
	}
	acct, err := s.auth.Authenticate(r.Context(), c.Value)
	switch {
	case err == nil:
		// Refresh the browser-side expiry to match the sliding server session.
		s.setCookie(w, c.Value, int(auth.IdleTimeout/time.Second))
		fmt.Fprintln(w, acct.Username)
	case errors.Is(err, auth.ErrNoSession):
		s.setCookie(w, "", -1)
		http.Error(w, "not logged in", http.StatusUnauthorized)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) setCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   !s.opts.InsecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) acquire(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case s.hashSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Server) release() { <-s.hashSlots }

func (s *Server) clientIP(r *http.Request) string {
	if s.opts.BehindProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
