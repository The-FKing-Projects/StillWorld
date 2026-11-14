package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRevokeSessionsLeavesCredentialsAndOtherAccountsAlone(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	name, _ := register(t, s, "player@example.invalid")
	other, _ := register(t, s, "")
	first, err := s.Login(ctx, name, "password123")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Login(ctx, name, "password123")
	if err != nil {
		t.Fatal(err)
	}
	otherToken, err := s.Login(ctx, other, "password123")
	if err != nil {
		t.Fatal(err)
	}
	resetToken, err := s.IssueResetToken(ctx, "player@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeSessions(ctx, strings.ToUpper(name)); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{first, second} {
		if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
			t.Fatalf("session survived revocation: %v", err)
		}
	}
	if _, err := s.Authenticate(ctx, otherToken); err != nil {
		t.Fatalf("another account's session was revoked: %v", err)
	}
	if _, err := s.Login(ctx, name, "password123"); err != nil {
		t.Fatalf("password changed or login disabled: %v", err)
	}
	if err := s.RedeemResetToken(ctx, resetToken, "newpassword123"); err != nil {
		t.Fatalf("recovery token was revoked: %v", err)
	}
	if err := s.RevokeSessions(ctx, "missinguser"); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("missing account: %v", err)
	}
	if err := s.RevokeSessions(ctx, "bad username"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid username: %v", err)
	}
}

func TestRevokeSessionsFailureRollsBack(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	name, _ := register(t, s, "")
	token, err := s.Login(ctx, name, "password123")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.ExecContext(ctx, `CREATE FUNCTION reject_revoke_delete() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test delete failure'; END $$;
		CREATE TRIGGER reject_revoke_delete BEFORE DELETE ON sessions
		FOR EACH ROW EXECUTE FUNCTION reject_revoke_delete()`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeSessions(ctx, name); err == nil {
		t.Fatal("revocation succeeded despite deletion failure")
	}
	if _, err := s.Authenticate(ctx, token); err != nil {
		t.Fatalf("session changed after rollback: %v", err)
	}
}

func TestRevokeSessionsDoesNotEnableDisabledAccount(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	name, _ := register(t, s, "")
	if err := s.SetAccountEnabled(ctx, name, false); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeSessions(ctx, name); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, name, "password123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("disabled account was enabled: %v", err)
	}
}

func TestRevokeSessionsReportsUncertainCommit(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	name, _ := register(t, s, "")
	token, err := s.Login(ctx, name, "password123")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.ExecContext(ctx, `CREATE FUNCTION reject_revoke_commit() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test commit failure'; END $$;
		CREATE CONSTRAINT TRIGGER reject_revoke_commit AFTER DELETE ON sessions
		DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_revoke_commit()`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeSessions(ctx, name); !errors.Is(err, ErrRevokeOutcomeUnknown) {
		t.Fatalf("commit error = %v", err)
	}
	if _, err := s.Authenticate(ctx, token); err != nil {
		t.Fatalf("session changed after rejected commit: %v", err)
	}
}

func TestRevokeSessionsWaitsForLogin(t *testing.T) {
	s, _ := newTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	name, id := register(t, s, "")
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var pid int
	if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := tx.QueryRowContext(ctx, `SELECT password_hash FROM accounts WHERE id = $1 FOR SHARE`, id).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- s.RevokeSessions(ctx, name) }()
	waitForAccountLock(t, ctx, s.DB, pid, result)
	token := "pending-session"
	now := s.Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions
		(token_hash, account_id, created_at, last_seen_at, expires_at) VALUES ($1, $2, $3, $3, $4)`,
		hashToken(token), id, now, now.Add(IdleTimeout)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("concurrent session survived revocation: %v", err)
	}
}
