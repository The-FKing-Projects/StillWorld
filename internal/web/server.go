// Package web is StillWorld's HTTP layer: signup, login, logout, and sessions
// carried in a cookie.
package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/auth"
)

// Authenticator is the account and session behaviour the HTTP layer needs.
type Authenticator interface {
	Register(ctx context.Context, username, email, password string) (int64, error)
	Login(ctx context.Context, username, password string) (string, error)
	Authenticate(ctx context.Context, token string) (auth.Account, error)
	Logout(ctx context.Context, token string) error
}

// Options configure a Server.
type Options struct {
	// InsecureCookies drops the Secure cookie flag for local development.
	InsecureCookies bool
	// BehindProxy trusts the rightmost X-Forwarded-For entry as the client IP.
	BehindProxy bool
	// Now is the clock; defaults to time.Now.
	Now func() time.Time
}

const (
	maxBodyBytes = 8 << 10
	// maxConcurrentHashes bounds simultaneous Argon2id work, since each hash
	// uses tens of MiB of memory.
	maxConcurrentHashes = 4

	loginWindow     = 15 * time.Minute
	maxLoginFailsIP = 20
	maxLoginFailsID = 10
	signupWindow    = time.Hour
	maxSignupsIP    = 5
)

// Server serves the HTTP API.
type Server struct {
	auth       Authenticator
	opts       Options
	hashSlots  chan struct{}
	loginIP    *limiter
	loginUser  *limiter
	signupIP   *limiter
	cookieName string
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
		auth:       a,
		opts:       opts,
		hashSlots:  make(chan struct{}, maxConcurrentHashes),
		loginIP:    newLimiter(maxLoginFailsIP, loginWindow, opts.Now),
		loginUser:  newLimiter(maxLoginFailsID, loginWindow, opts.Now),
		signupIP:   newLimiter(maxSignupsIP, signupWindow, opts.Now),
		cookieName: name,
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
			if !sameOrigin(r) {
				http.Error(w, "cross-site request rejected", http.StatusForbidden)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// sameOrigin rejects browser requests initiated by another site. Requests with
// neither header come from non-browser clients, which are not a CSRF vector.
func sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.signupIP.blocked(ip) {
		http.Error(w, "too many signups; try again later", http.StatusTooManyRequests)
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

	s.signupIP.add(ip)
	_, err := s.auth.Register(r.Context(), r.PostFormValue("username"), r.PostFormValue("email"), r.PostFormValue("password"))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintln(w, "account created")
	case errors.Is(err, auth.ErrInvalidInput):
		http.Error(w, strings.TrimPrefix(err.Error(), auth.ErrInvalidInput.Error()+": "), http.StatusBadRequest)
	case errors.Is(err, auth.ErrUsernameTaken):
		http.Error(w, "username already taken", http.StatusConflict)
	case errors.Is(err, auth.ErrEmailTaken):
		http.Error(w, "email already in use", http.StatusConflict)
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
	if s.loginIP.blocked(ip) || s.loginUser.blocked(username) {
		http.Error(w, "too many failed attempts; try again later", http.StatusTooManyRequests)
		return
	}
	if !s.acquire(r.Context()) {
		http.Error(w, "server busy", http.StatusServiceUnavailable)
		return
	}
	defer s.release()

	token, err := s.auth.Login(r.Context(), r.PostFormValue("username"), r.PostFormValue("password"))
	switch {
	case err == nil:
		s.loginUser.reset(username)
		s.setCookie(w, token, int(auth.IdleTimeout/time.Second))
		fmt.Fprintln(w, "logged in")
	case errors.Is(err, auth.ErrInvalidCredentials):
		s.loginIP.add(ip)
		s.loginUser.add(username)
		http.Error(w, "invalid username or password", http.StatusUnauthorized)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.cookieName); err == nil {
		if err := s.auth.Logout(r.Context(), c.Value); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	s.setCookie(w, "", -1)
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
	case s.hashSlots <- struct{}{}:
		return true
	case <-ctx.Done():
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
