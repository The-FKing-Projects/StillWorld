package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/db"
)

func newTestService(t *testing.T) (*Service, *time.Time) {
	t.Helper()
	url := os.Getenv("STILLWORLD_DATABASE_URL")
	if url == "" {
		t.Skip("STILLWORLD_DATABASE_URL not set")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	s, err := NewService(d)
	if err != nil {
		t.Fatal(err)
	}
	s.Params = testParams
	if err := s.init(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	s.Now = func() time.Time { return now }
	return s, &now
}

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano()%1_000_000_000_000)
}

func register(t *testing.T, s *Service, email string) (string, int64) {
	t.Helper()
	name := uniqueName("u")
	id, err := s.Register(context.Background(), name, email, "password123")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { s.DB.Exec(`DELETE FROM accounts WHERE id = $1`, id) })
	return name, id
}

func TestRegisterValidation(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	cases := []struct{ name, user, email, pw string }{
		{"short username", "ab", "", "password123"},
		{"bad characters", "bad name!", "", "password123"},
		{"short password", uniqueName("v"), "", "short"},
		{"huge password", uniqueName("v"), "", strings.Repeat("a", 257)},
		{"bad email", uniqueName("v"), "not-an-email", "password123"},
		{"email with display name", uniqueName("v"), "Bob <bob@example.com>", "password123"},
	}
	for _, c := range cases {
		if _, err := s.Register(ctx, c.user, c.email, c.pw); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", c.name, err)
		}
	}
}

func TestRegisterDuplicates(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	email := uniqueName("e") + "@example.com"
	name, _ := register(t, s, email)

	if _, err := s.Register(ctx, strings.ToUpper(name), "", "password123"); !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("duplicate username: %v", err)
	}
	if _, err := s.Register(ctx, uniqueName("d"), strings.ToUpper(email), "password123"); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("duplicate email: %v", err)
	}
}

func TestLoginAuthenticateLogout(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	name, id := register(t, s, "")

	if _, err := s.Login(ctx, name, "wrongpassword"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong password: %v", err)
	}
	if _, err := s.Login(ctx, "nobody"+name, "password123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("unknown user: %v", err)
	}
	if _, err := s.Login(ctx, name, strings.Repeat("a", 300)); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("oversized password: %v", err)
	}

	token, err := s.Login(ctx, strings.ToUpper(name), "password123")
	if err != nil {
		t.Fatalf("login (case-insensitive username): %v", err)
	}
	acct, err := s.Authenticate(ctx, token)
	if err != nil || acct.ID != id || acct.Username != name {
		t.Fatalf("authenticate = %+v, %v", acct, err)
	}

	var stored int
	s.DB.QueryRow(`SELECT count(*) FROM sessions WHERE token_hash = $1`, []byte(token)).Scan(&stored)
	if stored != 0 {
		t.Error("raw token must not be stored")
	}

	if err := s.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Errorf("after logout: %v", err)
	}
	if _, err := s.Authenticate(ctx, "garbage"); !errors.Is(err, ErrNoSession) {
		t.Errorf("garbage token: %v", err)
	}
}

func TestSessionExpiry(t *testing.T) {
	s, now := newTestService(t)
	ctx := context.Background()
	name, _ := register(t, s, "")
	start := *now

	token, err := s.Login(ctx, name, "password123")
	if err != nil {
		t.Fatal(err)
	}

	// Activity every 20 days keeps the session alive (sliding expiry).
	for _, days := range []int{20, 40, 60, 80} {
		*now = start.Add(time.Duration(days) * 24 * time.Hour)
		if _, err := s.Authenticate(ctx, token); err != nil {
			t.Fatalf("day %d: %v", days, err)
		}
	}

	// At day 80 the idle window would reach day 110, but the cap is day 90.
	var expires time.Time
	s.DB.QueryRow(`SELECT expires_at FROM sessions WHERE token_hash = $1`, hashToken(token)).Scan(&expires)
	if want := start.Add(MaxLifetime); !expires.Equal(want) {
		t.Errorf("expires_at = %v, want capped at %v", expires, want)
	}

	*now = start.Add(MaxLifetime + time.Second)
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Errorf("past hard cap: %v", err)
	}
}

func TestIdleExpiryAndCleanup(t *testing.T) {
	s, now := newTestService(t)
	ctx := context.Background()
	name, _ := register(t, s, "")
	token, err := s.Login(ctx, name, "password123")
	if err != nil {
		t.Fatal(err)
	}

	*now = now.Add(IdleTimeout + time.Second)
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Errorf("idle past 30 days: %v", err)
	}
	n, err := s.DeleteExpiredSessions(ctx)
	if err != nil || n < 1 {
		t.Errorf("DeleteExpiredSessions = %d, %v", n, err)
	}
}
