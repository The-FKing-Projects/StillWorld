package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/auth"
)

type fakeAuth struct {
	users    map[string]string // username -> password
	sessions map[string]string // token -> username
	logins   int
}

func newFakeAuth() *fakeAuth {
	return &fakeAuth{users: map[string]string{}, sessions: map[string]string{}}
}

func (f *fakeAuth) Register(_ context.Context, username, email, password string) (int64, error) {
	if len(username) < 3 {
		return 0, auth.ErrInvalidInput
	}
	if _, ok := f.users[username]; ok {
		return 0, auth.ErrUsernameTaken
	}
	f.users[username] = password
	return 1, nil
}

func (f *fakeAuth) Login(_ context.Context, username, password string) (string, error) {
	f.logins++
	if p, ok := f.users[username]; !ok || p != password {
		return "", auth.ErrInvalidCredentials
	}
	tok := "tok-" + username
	f.sessions[tok] = username
	return tok, nil
}

func (f *fakeAuth) Authenticate(_ context.Context, token string) (auth.Account, error) {
	if u, ok := f.sessions[token]; ok {
		return auth.Account{ID: 1, Username: u}, nil
	}
	return auth.Account{}, auth.ErrNoSession
}

func (f *fakeAuth) Logout(_ context.Context, token string) error {
	delete(f.sessions, token)
	return nil
}

func post(h http.Handler, path string, form url.Values, mutate func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Stillworld-Request", "1")
	r.Host = "game.example"
	r.RemoteAddr = "203.0.113.5:4000"
	if mutate != nil {
		mutate(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func get(h http.Handler, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.MaxAge >= 0 && c.Value != "" {
			return c
		}
	}
	t.Fatal("no session cookie set")
	return nil
}

func TestSignupLoginMeLogout(t *testing.T) {
	f := newFakeAuth()
	h := New(f, Options{}).Handler()

	if w := post(h, "/signup", url.Values{"username": {"alice"}, "password": {"password123"}}, nil); w.Code != http.StatusCreated {
		t.Fatalf("signup: %d %s", w.Code, w.Body)
	}
	if w := post(h, "/signup", url.Values{"username": {"alice"}, "password": {"password123"}}, nil); w.Code != http.StatusConflict {
		t.Fatalf("duplicate signup: %d", w.Code)
	}
	if w := post(h, "/signup", url.Values{"username": {"x"}, "password": {"password123"}}, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid signup: %d", w.Code)
	}

	w := post(h, "/login", url.Values{"username": {"alice"}, "password": {"password123"}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	c := sessionCookie(t, w)
	if c.Name != "__Host-session" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Fatalf("cookie attributes wrong: %+v", c)
	}

	if me := get(h, "/me", c); me.Code != http.StatusOK || strings.TrimSpace(me.Body.String()) != "alice" {
		t.Fatalf("me: %d %q", me.Code, me.Body)
	}
	if me := get(h, "/me"); me.Code != http.StatusUnauthorized {
		t.Fatalf("me without cookie: %d", me.Code)
	}

	out := post(h, "/logout", nil, func(r *http.Request) { r.AddCookie(c) })
	if out.Code != http.StatusOK {
		t.Fatalf("logout: %d", out.Code)
	}
	if me := get(h, "/me", c); me.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout: %d", me.Code)
	}
}

func TestInsecureCookiesForDev(t *testing.T) {
	f := newFakeAuth()
	f.users["alice"] = "password123"
	h := New(f, Options{InsecureCookies: true}).Handler()
	c := sessionCookie(t, post(h, "/login", url.Values{"username": {"alice"}, "password": {"password123"}}, nil))
	if c.Name != "session" || c.Secure {
		t.Fatalf("dev cookie wrong: %+v", c)
	}
}

func TestStaleCookieIsCleared(t *testing.T) {
	h := New(newFakeAuth(), Options{}).Handler()
	w := get(h, "/me", &http.Cookie{Name: "__Host-session", Value: "stale"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d", w.Code)
	}
	cleared := false
	for _, c := range w.Result().Cookies() {
		if c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("stale cookie should be cleared")
	}
}

func TestCrossSiteRequestsRejected(t *testing.T) {
	f := newFakeAuth()
	f.users["alice"] = "password123"
	h := New(f, Options{}).Handler()
	form := url.Values{"username": {"alice"}, "password": {"password123"}}

	cases := map[string]func(*http.Request){
		"cross-site fetch metadata": func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"same-site fetch metadata":  func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") },
		"foreign origin":            func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"foreign origin with port":  func(r *http.Request) { r.Header.Set("Origin", "https://game.example:8443") },
		"wrong scheme":              func(r *http.Request) { r.Header.Set("Origin", "http://game.example") },
		"no CSRF evidence":          func(r *http.Request) { r.Header.Del("X-Stillworld-Request") },
		"invalid origin path":       func(r *http.Request) { r.Header.Set("Origin", "https://game.example/path") },
	}
	for name, mut := range cases {
		if w := post(h, "/login", form, mut); w.Code != http.StatusForbidden {
			t.Errorf("%s: code = %d, want 403", name, w.Code)
		}
	}
	if f.logins != 0 {
		t.Errorf("rejected requests must not reach the authenticator, saw %d logins", f.logins)
	}

	allowed := map[string]func(*http.Request){
		"explicit client header": nil,
		"same origin": func(r *http.Request) {
			r.Header.Set("Origin", "https://game.example")
			r.Header.Set("Sec-Fetch-Site", "same-origin")
		},
	}
	for name, mut := range allowed {
		if w := post(h, "/login", form, mut); w.Code != http.StatusOK {
			t.Errorf("%s: code = %d, want 200", name, w.Code)
		}
	}
}

func TestBodyTooLarge(t *testing.T) {
	h := New(newFakeAuth(), Options{}).Handler()
	big := url.Values{"username": {"alice"}, "password": {strings.Repeat("a", 20_000)}}
	if w := post(h, "/signup", big, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("oversized body: %d", w.Code)
	}
}

func TestResponseHeaders(t *testing.T) {
	w := get(New(newFakeAuth(), Options{}).Handler(), "/healthz")
	for k, want := range map[string]string{
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
		"Content-Type":           "text/plain; charset=utf-8",
	} {
		if got := w.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestLoginRateLimitPerUsername(t *testing.T) {
	now := time.Now()
	f := newFakeAuth()
	f.users["alice"] = "password123"
	h := New(f, Options{Now: func() time.Time { return now }}).Handler()
	bad := url.Values{"username": {"Alice"}, "password": {"wrong"}}

	for i := 0; i < maxLoginFailsID; i++ {
		// Vary the IP so only the per-username limit can trigger.
		ip := "198.51.100." + string(rune('1'+i)) + ":1"
		if w := post(h, "/login", bad, func(r *http.Request) { r.RemoteAddr = ip }); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	good := url.Values{"username": {"alice"}, "password": {"password123"}}
	if w := post(h, "/login", good, func(r *http.Request) { r.RemoteAddr = "192.0.2.9:1" }); w.Code != http.StatusTooManyRequests {
		t.Fatalf("locked out username: %d", w.Code)
	}

	now = now.Add(loginWindow + time.Second)
	if w := post(h, "/login", good, func(r *http.Request) { r.RemoteAddr = "192.0.2.9:1" }); w.Code != http.StatusOK {
		t.Fatalf("after window: %d", w.Code)
	}
}

func TestLoginRateLimitPerIP(t *testing.T) {
	f := newFakeAuth()
	h := New(f, Options{}).Handler()
	for i := 0; i < maxLoginFailsIP; i++ {
		// A different username each time so only the IP limit can trigger.
		form := url.Values{"username": {"user" + string(rune('a'+i))}, "password": {"x"}}
		if w := post(h, "/login", form, nil); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	form := url.Values{"username": {"fresh"}, "password": {"x"}}
	if w := post(h, "/login", form, nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("IP not limited: %d", w.Code)
	}
	if w := post(h, "/login", form, func(r *http.Request) { r.RemoteAddr = "192.0.2.77:1" }); w.Code != http.StatusUnauthorized {
		t.Fatalf("other IP should not be limited: %d", w.Code)
	}
}

func TestSuccessfulLoginResetsUsernameCounter(t *testing.T) {
	f := newFakeAuth()
	f.users["alice"] = "password123"
	h := New(f, Options{}).Handler()
	bad := url.Values{"username": {"alice"}, "password": {"wrong"}}
	good := url.Values{"username": {"alice"}, "password": {"password123"}}

	for round := 0; round < 3; round++ {
		for i := 0; i < maxLoginFailsID-1; i++ {
			post(h, "/login", bad, func(r *http.Request) { r.RemoteAddr = "192.0.2." + string(rune('1'+i)) + ":1" })
		}
		if w := post(h, "/login", good, func(r *http.Request) { r.RemoteAddr = "192.0.2.99:1" }); w.Code != http.StatusOK {
			t.Fatalf("round %d: %d", round, w.Code)
		}
	}
}

func TestSignupRateLimit(t *testing.T) {
	h := New(newFakeAuth(), Options{}).Handler()
	for i := 0; i < maxSignupsIP; i++ {
		form := url.Values{"username": {"player" + string(rune('a'+i))}, "password": {"password123"}}
		if w := post(h, "/signup", form, nil); w.Code != http.StatusCreated {
			t.Fatalf("signup %d: %d", i, w.Code)
		}
	}
	form := url.Values{"username": {"onemore"}, "password": {"password123"}}
	if w := post(h, "/signup", form, nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")

	if got := New(nil, Options{}).clientIP(r); got != "10.0.0.1" {
		t.Errorf("direct: %q", got)
	}
	// Only the rightmost entry is trusted: earlier ones are client-supplied.
	if got := New(nil, Options{BehindProxy: true}).clientIP(r); got != "203.0.113.9" {
		t.Errorf("behind proxy: %q", got)
	}
}

func TestLimiterBoundsMemory(t *testing.T) {
	now := time.Now()
	l := newLimiter(1, time.Hour, func() time.Time { return now })
	l.maxKeys = 3
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if e := l.reserve(k); e != nil {
			l.finish(e, true, false)
		}
	}
	if len(l.entries) > 3 {
		t.Fatalf("entries = %d, want <= 3", len(l.entries))
	}
	now = now.Add(2 * time.Hour)
	e := l.reserve("f")
	if e == nil {
		t.Fatal("expired entries should allow a new key")
	}
	l.finish(e, true, false)
	if len(l.entries) != 1 {
		t.Fatalf("expired entries should be purged, have %d", len(l.entries))
	}
}

func TestLimiterConcurrentAdmissions(t *testing.T) {
	l := newLimiter(10, time.Hour, time.Now)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var allowed atomic.Int32
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if e := l.reserve("same"); e != nil {
				allowed.Add(1)
				l.finish(e, true, false)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := allowed.Load(); got != 10 {
		t.Fatalf("admitted %d requests, want 10", got)
	}
}

func TestLimiterFullFailsClosed(t *testing.T) {
	l := newLimiter(1, time.Hour, time.Now)
	l.maxKeys = 1
	e := l.reserve("first")
	if e == nil {
		t.Fatal("first admission failed")
	}
	if l.reserve("new") != nil {
		t.Fatal("new key bypassed a full limiter")
	}
	l.finish(e, true, false)
}

func TestHashAdmissionDoesNotQueue(t *testing.T) {
	s := New(nil, Options{})
	for i := 0; i < maxConcurrentHashes; i++ {
		if !s.acquire(context.Background()) {
			t.Fatal("failed to fill hash slots")
		}
	}
	if s.acquire(context.Background()) {
		t.Fatal("admitted request when all slots are occupied")
	}
	for i := 0; i < maxConcurrentHashes; i++ {
		s.release()
	}
}

func TestOriginSchemeForLocalAndProxy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		opts   Options
		origin string
		want   int
	}{
		{"local HTTP", Options{InsecureCookies: true}, "http://game.example", http.StatusOK},
		{"local wrong scheme", Options{InsecureCookies: true}, "https://game.example", http.StatusForbidden},
		{"proxy HTTPS", Options{BehindProxy: true}, "https://game.example", http.StatusOK},
		{"proxy wrong scheme", Options{BehindProxy: true}, "http://game.example", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAuth()
			f.users["alice"] = "password123"
			w := post(New(f, tc.opts).Handler(), "/login",
				url.Values{"username": {"alice"}, "password": {"password123"}},
				func(r *http.Request) { r.Header.Set("Origin", tc.origin) })
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}
