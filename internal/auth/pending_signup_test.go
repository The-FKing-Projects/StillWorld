package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/The-FKing-Projects/StillWorld/internal/testdb"
)

func TestPendingSignupDefersAccountUntilCode(t *testing.T) {
	database := testdb.Open(t)
	service, err := NewService(database)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	code, email, err := service.StartPendingSignup(ctx, "alice", "alice@example.com", "password123")
	if err != nil || code == "" || email != "alice@example.com" {
		t.Fatalf("start pending: code=%q email=%q err=%v", code, email, err)
	}
	if _, err := service.Login(ctx, "alice", "password123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("pending account is loginable: %v", err)
	}
	id, err := service.CompletePendingSignup(ctx, code)
	if err != nil || id == 0 {
		t.Fatalf("complete pending: %v", err)
	}
	if _, err := service.Login(ctx, "alice", "password123"); err != nil {
		t.Fatalf("login after complete: %v", err)
	}
	var verified bool
	if err := database.QueryRowContext(ctx, `SELECT email_verified_at IS NOT NULL AND verified_email = email FROM accounts WHERE id = $1`, id).
		Scan(&verified); err != nil || !verified {
		t.Fatalf("email not verified on create: verified=%v err=%v", verified, err)
	}
	if _, err := service.CompletePendingSignup(ctx, code); !errors.Is(err, ErrInvalidPendingSignup) {
		t.Fatalf("reused code: %v", err)
	}
}

func TestPendingSignupHidesExistingEmail(t *testing.T) {
	database := testdb.Open(t)
	service, err := NewService(database)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := service.Register(ctx, "owner", "taken@example.com", "password123"); err != nil {
		t.Fatal(err)
	}
	code, email, err := service.StartPendingSignup(ctx, "intruder", "TAKEN@example.com", "password123")
	if !errors.Is(err, ErrPendingSignupUnavailable) || code != "" || email != "" {
		t.Fatalf("existing email: code=%q email=%q err=%v", code, email, err)
	}
	if _, err := service.Login(ctx, "intruder", "password123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("intruder account created: %v", err)
	}
}

func TestPendingSignupRejectsTakenUsername(t *testing.T) {
	database := testdb.Open(t)
	service, err := NewService(database)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := service.Register(ctx, "alice", "", "password123"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.StartPendingSignup(ctx, "Alice", "new@example.com", "password123"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("username taken: %v", err)
	}
}

func TestPendingSignupReplacesPriorPendingForEmail(t *testing.T) {
	database := testdb.Open(t)
	service, err := NewService(database)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, _, err := service.StartPendingSignup(ctx, "alice", "alice@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := service.StartPendingSignup(ctx, "alice", "alice@example.com", "password456")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("replacement reused signup code")
	}
	if _, err := service.CompletePendingSignup(ctx, first); !errors.Is(err, ErrInvalidPendingSignup) {
		t.Fatalf("old code remained valid: %v", err)
	}
	if _, err := service.CompletePendingSignup(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Login(ctx, "alice", "password456"); err != nil {
		t.Fatalf("login with replaced password: %v", err)
	}
}

func TestPendingSignupInvalidInput(t *testing.T) {
	database := testdb.Open(t)
	service, err := NewService(database)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, _, err := service.StartPendingSignup(ctx, "ab", "a@example.com", "password123"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("short username: %v", err)
	}
	if _, _, err := service.StartPendingSignup(ctx, "alice", "not-an-email", "password123"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad email: %v", err)
	}
	if _, err := service.CompletePendingSignup(ctx, strings.Repeat("x", 10)); !errors.Is(err, ErrInvalidPendingSignup) {
		t.Fatalf("short code: %v", err)
	}
}
