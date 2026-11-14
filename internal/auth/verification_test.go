package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestEmailVerificationGatesRecoveryAndBindsAccount(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	name, id := register(t, s, "player@example.invalid")
	_, other := register(t, s, "other@example.invalid")
	if _, err := s.IssueResetToken(ctx, "player@example.invalid"); !errors.Is(err, ErrRecoveryUnavailable) {
		t.Fatalf("unverified account eligible for recovery: %v", err)
	}
	old, address, err := s.IssueEmailVerificationToken(ctx, id)
	if err != nil || address != "player@example.invalid" {
		t.Fatalf("issue code: %q %v", address, err)
	}
	code, _, err := s.IssueEmailVerificationToken(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []struct {
		account int64
		code    string
	}{{id, old}, {other, code}, {id, "bad"}} {
		if err := s.VerifyEmail(ctx, invalid.account, invalid.code); !errors.Is(err, ErrInvalidVerificationToken) {
			t.Fatalf("accepted invalid verification: %v", err)
		}
	}
	var hash []byte
	if err := s.DB.QueryRowContext(ctx, `SELECT token_hash FROM email_verification_tokens WHERE account_id = $1`, id).Scan(&hash); err != nil || string(hash) != string(hashToken(code)) {
		t.Fatalf("token hash: %v", err)
	}
	if err := s.VerifyEmail(ctx, id, code); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyEmail(ctx, id, code); !errors.Is(err, ErrInvalidVerificationToken) {
		t.Fatalf("accepted reused code: %v", err)
	}
	address, verified, err := s.EmailVerificationStatus(ctx, id)
	if err != nil || address != "player@example.invalid" || !verified {
		t.Fatalf("verification status: %q %v %v", address, verified, err)
	}
	reset, err := s.IssueResetToken(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RedeemResetToken(ctx, reset, "newpassword123"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, name, "newpassword123"); err != nil {
		t.Fatal(err)
	}
}

func TestEmailVerificationRejectsExpiredChangedOrDisabledAccounts(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	name, id := register(t, s, "player@example.invalid")
	code, _, err := s.IssueEmailVerificationToken(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE email_verification_tokens SET expires_at = clock_timestamp() - interval '1 second' WHERE account_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyEmail(ctx, id, code); !errors.Is(err, ErrInvalidVerificationToken) {
		t.Fatalf("expired code: %v", err)
	}
	code, _, err = s.IssueEmailVerificationToken(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE accounts SET email = 'changed@example.invalid' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyEmail(ctx, id, code); !errors.Is(err, ErrInvalidVerificationToken) {
		t.Fatalf("code accepted for changed address: %v", err)
	}
	code, _, err = s.IssueEmailVerificationToken(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountEnabled(ctx, name, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountEnabled(ctx, name, true); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyEmail(ctx, id, code); !errors.Is(err, ErrInvalidVerificationToken) {
		t.Fatalf("code survived disable: %v", err)
	}
}

func TestEmailVerificationConcurrentSingleUse(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	_, id := register(t, s, "player@example.invalid")
	code, _, err := s.IssueEmailVerificationToken(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.VerifyEmail(ctx, id, code) }()
	}
	wg.Wait()
	close(results)
	success, rejected := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrInvalidVerificationToken) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || rejected != 1 {
		t.Fatalf("success=%d rejected=%d", success, rejected)
	}
}

func TestChangedAddressCannotInheritVerificationOrOldReset(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	_, id := registerVerified(t, s, "old@example.invalid")
	oldReset, err := s.IssueResetToken(ctx, "old@example.invalid")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.DB.ExecContext(ctx, `UPDATE accounts SET email = 'new@example.invalid' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IssueResetToken(ctx, "new@example.invalid"); !errors.Is(err, ErrRecoveryUnavailable) {
		t.Fatalf("changed address inherited verification: %v", err)
	}
	if err := s.RedeemResetToken(ctx, oldReset, "newpassword123"); !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("old reset accepted after address change: %v", err)
	}
	code, _, err := s.IssueEmailVerificationToken(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyEmail(ctx, id, code); err != nil {
		t.Fatal(err)
	}
	if err := s.RedeemResetToken(ctx, oldReset, "newpassword123"); !errors.Is(err, ErrInvalidResetToken) {
		t.Fatalf("old reset accepted after new address verification: %v", err)
	}
}

func TestAdministratorPasswordResetRevokesVerificationCode(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	name, id := register(t, s, "player@example.invalid")
	code, _, err := s.IssueEmailVerificationToken(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ResetPassword(ctx, name, "newpassword123"); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyEmail(ctx, id, code); !errors.Is(err, ErrInvalidVerificationToken) {
		t.Fatalf("verification code survived password reset: %v", err)
	}
}
