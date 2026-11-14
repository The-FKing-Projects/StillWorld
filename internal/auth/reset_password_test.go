package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestResetPasswordRevokesSessions(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	name, _ := register(t, s, "")
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
	if err := s.ResetPassword(ctx, name, "short"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid password: %v", err)
	}
	if _, err := s.Authenticate(ctx, first); err != nil {
		t.Fatalf("invalid reset revoked a session: %v", err)
	}
	if err := s.ResetPassword(ctx, strings.ToUpper(name), "newpassword123"); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{first, second} {
		if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
			t.Fatalf("session survived reset: %v", err)
		}
	}
	if _, err := s.Authenticate(ctx, otherToken); err != nil {
		t.Fatalf("reset revoked another account's session: %v", err)
	}
	if _, err := s.Login(ctx, name, "password123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password: %v", err)
	}
	if _, err := s.Login(ctx, name, "newpassword123"); err != nil {
		t.Fatalf("new password: %v", err)
	}
	if err := s.ResetPassword(ctx, "missinguser", "newpassword123"); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("missing account: %v", err)
	}
}

func TestResetPasswordRollsBackWhenSessionRevocationFails(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	name, _ := register(t, s, "")
	token, err := s.Login(ctx, name, "password123")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.ExecContext(ctx, `CREATE FUNCTION reject_session_delete() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test delete failure'; END $$;
		CREATE TRIGGER reject_session_delete BEFORE DELETE ON sessions
		FOR EACH ROW EXECUTE FUNCTION reject_session_delete()`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ResetPassword(ctx, name, "newpassword123"); err == nil {
		t.Fatal("reset succeeded despite session deletion failure")
	}
	if _, err := s.Authenticate(ctx, token); err != nil {
		t.Fatalf("session changed after rollback: %v", err)
	}
	if _, err := s.Login(ctx, name, "password123"); err != nil {
		t.Fatalf("old password changed after rollback: %v", err)
	}
}

func TestResetPasswordReportsUncertainCommit(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	name, _ := register(t, s, "")
	_, err := s.DB.ExecContext(ctx, `CREATE FUNCTION reject_password_commit() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test commit failure'; END $$;
		CREATE CONSTRAINT TRIGGER reject_password_commit AFTER UPDATE ON accounts
		DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_password_commit()`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ResetPassword(ctx, name, "newpassword123"); !errors.Is(err, ErrResetOutcomeUnknown) {
		t.Fatalf("commit error = %v", err)
	}
	if _, err := s.Login(ctx, name, "password123"); err != nil {
		t.Fatalf("old password changed after failed commit: %v", err)
	}
}

func TestLoginCannotCreateSessionFromStalePassword(t *testing.T) {
	s, _ := newTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	name, id := register(t, s, "")
	var oldHash string
	if err := s.DB.QueryRowContext(ctx, `SELECT password_hash FROM accounts WHERE id = $1`, id).Scan(&oldHash); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var pid int
	if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	newHash, err := HashPassword("newpassword123", testParams)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET password_hash = $1 WHERE id = $2`, newHash, id); err != nil {
		t.Fatal(err)
	}
	loginResult := make(chan error, 1)
	go func() {
		_, err := s.createSession(ctx, id, oldHash)
		loginResult <- err
	}()
	waitForAccountLock(t, ctx, s.DB, pid, loginResult)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-loginResult; !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("login with stale password: %v", err)
	}
	if _, err := s.Login(ctx, name, "newpassword123"); err != nil {
		t.Fatalf("new password: %v", err)
	}
}

func TestResetWaitsForLoginAndRevokesItsSession(t *testing.T) {
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
	resetResult := make(chan error, 1)
	go func() { resetResult <- s.ResetPassword(ctx, name, "newpassword123") }()
	waitForAccountLock(t, ctx, s.DB, pid, resetResult)
	token := "test-pending-session"
	now := s.Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions
		(token_hash, account_id, created_at, last_seen_at, expires_at) VALUES ($1, $2, $3, $3, $4)`,
		hashToken(token), id, now, now.Add(IdleTimeout)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-resetResult; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("concurrent login's session survived reset: %v", err)
	}
}

func waitForAccountLock(t *testing.T, ctx context.Context, db *sql.DB, pid int, result <-chan error) {
	t.Helper()
	for {
		var blocked bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case err := <-result:
			t.Fatalf("operation completed before account lock was released: %v", err)
		case <-ctx.Done():
			t.Fatal("operation did not wait for account lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
