package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDisableAndEnableAccount(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	name, id := register(t, s, "")
	other, _ := register(t, s, "")
	token, err := s.Login(ctx, name, "password123")
	if err != nil {
		t.Fatal(err)
	}
	otherToken, err := s.Login(ctx, other, "password123")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountEnabled(ctx, strings.ToUpper(name), false); err != nil {
		t.Fatal(err)
	}
	var disabledAt time.Time
	if err := s.DB.QueryRowContext(ctx, `SELECT disabled_at FROM accounts WHERE id = $1`, id).Scan(&disabledAt); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountEnabled(ctx, name, false); err != nil {
		t.Fatal(err)
	}
	var repeatedAt time.Time
	if err := s.DB.QueryRowContext(ctx, `SELECT disabled_at FROM accounts WHERE id = $1`, id).Scan(&repeatedAt); err != nil || !repeatedAt.Equal(disabledAt) {
		t.Fatalf("repeated disable changed timestamp: %v, %v", repeatedAt, err)
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("disabled session: %v", err)
	}
	if _, err := s.Authenticate(ctx, otherToken); err != nil {
		t.Fatalf("other account session: %v", err)
	}
	if _, err := s.Login(ctx, name, "password123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("disabled login: %v", err)
	}
	now := s.Now()
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO sessions
		(token_hash, account_id, created_at, last_seen_at, expires_at) VALUES ($1, $2, $3, $3, $4)`,
		hashToken("unexpected-session"), id, now, now.Add(IdleTimeout)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, "unexpected-session"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("disabled account authenticated an unexpected session: %v", err)
	}
	if err := s.ResetPassword(ctx, name, "newpassword123"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, name, "newpassword123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("password reset re-enabled the account: %v", err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO sessions
		(token_hash, account_id, created_at, last_seen_at, expires_at) VALUES ($1, $2, $3, $3, $4)`,
		hashToken("unexpected-session"), id, now, now.Add(IdleTimeout)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountEnabled(ctx, name, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("enable restored an old session: %v", err)
	}
	if _, err := s.Authenticate(ctx, "unexpected-session"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("enable restored an unexpected session: %v", err)
	}
	newToken, err := s.Login(ctx, name, "newpassword123")
	if err != nil {
		t.Fatalf("enabled login: %v", err)
	}
	if err := s.SetAccountEnabled(ctx, name, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, newToken); err != nil {
		t.Fatalf("repeated enable revoked an active session: %v", err)
	}
	if err := s.SetAccountEnabled(ctx, "missinguser", false); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("missing account: %v", err)
	}
}

func TestDisableRollsBackWhenSessionRevocationFails(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	name, _ := register(t, s, "")
	token, err := s.Login(ctx, name, "password123")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.ExecContext(ctx, `CREATE FUNCTION reject_disable_delete() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test delete failure'; END $$;
		CREATE TRIGGER reject_disable_delete BEFORE DELETE ON sessions
		FOR EACH ROW EXECUTE FUNCTION reject_disable_delete()`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountEnabled(ctx, name, false); err == nil {
		t.Fatal("disable succeeded despite session deletion failure")
	}
	if _, err := s.Authenticate(ctx, token); err != nil {
		t.Fatalf("session changed after rollback: %v", err)
	}
	if _, err := s.Login(ctx, name, "password123"); err != nil {
		t.Fatalf("account remained disabled after rollback: %v", err)
	}
}

func TestDisableReportsUncertainCommit(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	name, _ := register(t, s, "")
	_, err := s.DB.ExecContext(ctx, `CREATE FUNCTION reject_disable_commit() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test commit failure'; END $$;
		CREATE CONSTRAINT TRIGGER reject_disable_commit AFTER UPDATE ON accounts
		DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_disable_commit()`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountEnabled(ctx, name, false); !errors.Is(err, ErrAccountStatusUnknown) {
		t.Fatalf("commit error = %v", err)
	}
}

func TestLoginWaitsForDisableAndRejectsSession(t *testing.T) {
	s, _ := newTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, id := register(t, s, "")
	var hash string
	if err := s.DB.QueryRowContext(ctx, `SELECT password_hash FROM accounts WHERE id = $1`, id).Scan(&hash); err != nil {
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
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET disabled_at = now() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := s.createSession(ctx, id, hash)
		result <- err
	}()
	waitForAccountLock(t, ctx, s.DB, pid, result)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("concurrent login: %v", err)
	}
}

func TestDisableWaitsForLoginAndRevokesSession(t *testing.T) {
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
	go func() { result <- s.SetAccountEnabled(ctx, name, false) }()
	waitForAccountLock(t, ctx, s.DB, pid, result)
	token := "pending-disable-session"
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
		t.Fatalf("session survived concurrent disable: %v", err)
	}
}
