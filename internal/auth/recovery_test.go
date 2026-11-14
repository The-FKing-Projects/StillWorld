package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResetTokenSingleUseAndSessionRevocation(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	email := uniqueName("email") + "@example.invalid"
	name, id := register(t, s, email)
	session, err := s.Login(ctx, name, "password123")
	if err != nil {
		t.Fatal(err)
	}
	oldToken, err := s.IssueResetToken(ctx, strings.ToUpper(email))
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.IssueResetToken(ctx, email)
	if err != nil || token == oldToken {
		t.Fatalf("replacement token issuance failed: %v", err)
	}
	var hash []byte
	var remaining float64
	if err := s.DB.QueryRowContext(ctx, `SELECT token_hash, extract(epoch FROM expires_at - clock_timestamp())
		FROM password_reset_tokens WHERE account_id = $1`, id).Scan(&hash, &remaining); err != nil {
		t.Fatal(err)
	}
	if string(hash) == token || string(hash) != string(hashToken(token)) || remaining <= 29*60 || remaining > 30*60 {
		t.Fatal("token was not stored as a short-lived hash")
	}
	if err := s.RedeemResetToken(ctx, oldToken, "newpassword123"); !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("replaced token: %v", err)
	}
	if err := s.RedeemResetToken(ctx, token, "short"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid password: %v", err)
	}
	if err := s.RedeemResetToken(ctx, token, "newpassword123"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, session); !errors.Is(err, ErrNoSession) {
		t.Fatalf("session survived reset: %v", err)
	}
	if _, err := s.Login(ctx, name, "password123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password: %v", err)
	}
	if _, err := s.Login(ctx, name, "newpassword123"); err != nil {
		t.Fatalf("new password: %v", err)
	}
	if err := s.RedeemResetToken(ctx, token, "newpassword123"); !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("replayed token: %v", err)
	}
	for _, invalid := range []string{"", strings.Repeat("x", 43), token + "x"} {
		if err := s.RedeemResetToken(ctx, invalid, "newpassword123"); !errors.Is(err, ErrInvalidResetToken) {
			t.Errorf("invalid token accepted: %v", err)
		}
	}
}

func TestResetTokenExpiryAndEligibility(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	email := uniqueName("email") + "@example.invalid"
	name, id := register(t, s, email)
	for _, address := range []string{"", "missing@example.invalid", strings.Repeat("a", maxEmailLen+1)} {
		if _, err := s.IssueResetToken(ctx, address); !errors.Is(err, ErrRecoveryUnavailable) {
			t.Fatalf("ineligible address: %v", err)
		}
	}
	token, err := s.IssueResetToken(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE password_reset_tokens SET expires_at = clock_timestamp() - interval '1 second'
		WHERE account_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.RedeemResetToken(ctx, token, "newpassword123"); !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("expired token: %v", err)
	}
	if err := s.SetAccountEnabled(ctx, name, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IssueResetToken(ctx, email); !errors.Is(err, ErrRecoveryUnavailable) {
		t.Fatalf("disabled account: %v", err)
	}
}

func TestAdminChangesInvalidateResetTokens(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	email := uniqueName("email") + "@example.invalid"
	name, _ := register(t, s, email)
	token, err := s.IssueResetToken(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ResetPassword(ctx, name, "adminpassword123"); err != nil {
		t.Fatal(err)
	}
	if err := s.RedeemResetToken(ctx, token, "newpassword123"); !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("token survived admin reset: %v", err)
	}
	token, err = s.IssueResetToken(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountEnabled(ctx, name, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountEnabled(ctx, name, true); err != nil {
		t.Fatal(err)
	}
	if err := s.RedeemResetToken(ctx, token, "newpassword123"); !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("token survived disable and enable: %v", err)
	}
}

func TestAdminChangesRollBackWhenTokenRevocationFails(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	email := uniqueName("email") + "@example.invalid"
	name, _ := register(t, s, email)
	token, err := s.IssueResetToken(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.ExecContext(ctx, `CREATE FUNCTION reject_token_delete() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test delete failure'; END $$;
		CREATE TRIGGER reject_token_delete BEFORE DELETE ON password_reset_tokens
		FOR EACH ROW EXECUTE FUNCTION reject_token_delete()`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ResetPassword(ctx, name, "adminpassword123"); err == nil {
		t.Fatal("admin reset succeeded despite token deletion failure")
	}
	if err := s.SetAccountEnabled(ctx, name, false); err == nil {
		t.Fatal("account disable succeeded despite token deletion failure")
	}
	if _, err := s.Login(ctx, name, "password123"); err != nil {
		t.Fatalf("admin changes did not roll back: %v", err)
	}
	if _, err := s.DB.ExecContext(ctx, `DROP TRIGGER reject_token_delete ON password_reset_tokens`); err != nil {
		t.Fatal(err)
	}
	if err := s.RedeemResetToken(ctx, token, "newpassword123"); err != nil {
		t.Fatalf("token not preserved after rollback: %v", err)
	}
}

func TestResetTokenIssuanceDoesNotReturnTokenOnCommitFailure(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	email := uniqueName("email") + "@example.invalid"
	register(t, s, email)
	_, err := s.DB.ExecContext(ctx, `CREATE FUNCTION reject_token_commit() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test commit failure'; END $$;
		CREATE CONSTRAINT TRIGGER reject_token_commit AFTER INSERT OR UPDATE ON password_reset_tokens
		DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_token_commit()`)
	if err != nil {
		t.Fatal(err)
	}
	if token, err := s.IssueResetToken(ctx, email); token != "" || err == nil {
		t.Fatalf("issuance returned a token or no error after commit failure: %v", err)
	}
}

func TestResetTokenConcurrentRedemption(t *testing.T) {
	s, _ := newTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	email := uniqueName("email") + "@example.invalid"
	name, _ := register(t, s, email)
	token, err := s.IssueResetToken(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results <- s.RedeemResetToken(ctx, token, fmt.Sprintf("newpassword%d", i))
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	success, rejected := 0, 0
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrInvalidResetToken):
			rejected++
		default:
			t.Fatalf("unexpected redemption error: %v", err)
		}
	}
	if success != 1 || rejected != 1 {
		t.Fatalf("successful=%d rejected=%d", success, rejected)
	}
	if _, err0 := s.Login(ctx, name, "newpassword0"); err0 != nil {
		if _, err1 := s.Login(ctx, name, "newpassword1"); err1 != nil {
			t.Fatal("neither successfully submitted password works")
		}
	}
}

func TestResetTokenReportsUncertainCommit(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	email := uniqueName("email") + "@example.invalid"
	name, _ := register(t, s, email)
	token, err := s.IssueResetToken(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.ExecContext(ctx, `CREATE FUNCTION reject_recovery_commit() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test commit failure'; END $$;
		CREATE CONSTRAINT TRIGGER reject_recovery_commit AFTER UPDATE ON accounts
		DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_recovery_commit()`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RedeemResetToken(ctx, token, "newpassword123"); !errors.Is(err, ErrTokenResetUnknown) {
		t.Fatalf("commit error = %v", err)
	}
	if _, err := s.Login(ctx, name, "password123"); err != nil {
		t.Fatalf("old password changed after failed commit: %v", err)
	}
}

func TestResetTokenRollbackPreservesTokenAndSessions(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	email := uniqueName("email") + "@example.invalid"
	name, _ := register(t, s, email)
	session, err := s.Login(ctx, name, "password123")
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.IssueResetToken(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.ExecContext(ctx, `CREATE FUNCTION reject_recovery_delete() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test delete failure'; END $$;
		CREATE TRIGGER reject_recovery_delete BEFORE DELETE ON sessions
		FOR EACH ROW EXECUTE FUNCTION reject_recovery_delete()`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RedeemResetToken(ctx, token, "newpassword123"); err == nil {
		t.Fatal("reset succeeded despite session deletion failure")
	}
	if _, err := s.Authenticate(ctx, session); err != nil {
		t.Fatalf("session changed after rollback: %v", err)
	}
	if _, err := s.Login(ctx, name, "password123"); err != nil {
		t.Fatalf("password changed after rollback: %v", err)
	}
	if _, err := s.DB.ExecContext(ctx, `DROP TRIGGER reject_recovery_delete ON sessions`); err != nil {
		t.Fatal(err)
	}
	if err := s.RedeemResetToken(ctx, token, "newpassword123"); err != nil {
		t.Fatalf("token not preserved after rollback: %v", err)
	}
}

func TestResetTokenExpiresWhileWaitingForAccountLock(t *testing.T) {
	s, _ := newTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	email := uniqueName("email") + "@example.invalid"
	_, id := register(t, s, email)
	token, err := s.IssueResetToken(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var pid, lockedID int
	if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id = $1 FOR UPDATE`, id).Scan(&lockedID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- s.RedeemResetToken(ctx, token, "newpassword123") }()
	waitForAccountLock(t, ctx, s.DB, pid, result)
	if _, err := s.DB.ExecContext(ctx, `UPDATE password_reset_tokens SET expires_at = clock_timestamp() - interval '1 second'
		WHERE account_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("token expired while waiting: %v", err)
	}
}
