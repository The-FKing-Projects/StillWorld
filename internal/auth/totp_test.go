package auth

import (
	"bytes"
	"context"
	"encoding/base32"
	"errors"
	"strings"
	"testing"
	"time"
)

func testTOTPRing(t *testing.T) *TOTPKeyRing {
	t.Helper()
	key := bytes.Repeat([]byte{0x11}, 32)
	ring, err := NewTOTPKeyRing("v1", map[string][]byte{"v1": key})
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

func TestTOTPCodeRoundTrip(t *testing.T) {
	secret := bytes.Repeat([]byte{0x42}, 20)
	now := time.Unix(1_700_000_000, 0).UTC()
	step := currentTOTPStep(now)
	code := totpCode(secret, step)
	got, ok := verifyTOTPCode(secret, code, now, -1)
	if !ok || got != step {
		t.Fatalf("verify = %d %v", got, ok)
	}
	if _, ok := verifyTOTPCode(secret, code, now, step); ok {
		t.Fatal("replay accepted")
	}
	if _, ok := verifyTOTPCode(secret, "000000", now, -1); ok {
		t.Fatal("wrong code accepted")
	}
}

func TestTOTPEnrolLoginDisable(t *testing.T) {
	s, now := newTestService(t)
	s.SetTOTPKeys(testTOTPRing(t))
	ctx := context.Background()
	name, id := register(t, s, "")

	enrol, err := s.BeginTOTPEnrolment(ctx, id, name)
	if err != nil || enrol.Secret == "" || !strings.Contains(enrol.URL, "otpauth://totp/") {
		t.Fatalf("begin: %#v %v", enrol, err)
	}
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrol.Secret)
	if err != nil {
		t.Fatal(err)
	}
	code := totpCode(raw, currentTOTPStep(*now))
	codes, err := s.ConfirmTOTPEnrolment(ctx, id, code)
	if err != nil || len(codes) != recoveryCodeCount {
		t.Fatalf("confirm: %v codes=%d", err, len(codes))
	}

	_, err = s.Login(ctx, name, "password123")
	var challenge *ChallengeError
	if !errors.As(err, &challenge) || challenge.Token == "" {
		t.Fatalf("login challenge: %v", err)
	}
	// Advance past the enrolment confirmation step so replay protection allows login.
	*now = now.Add(totpPeriod * time.Second)
	token, err := s.CompleteLoginTOTP(ctx, challenge.Token, totpCode(raw, currentTOTPStep(*now)))
	if err != nil {
		t.Fatalf("complete totp: %v", err)
	}
	if _, err := s.Authenticate(ctx, token); err != nil {
		t.Fatalf("authenticate: %v", err)
	}

	_, err = s.Login(ctx, name, "password123")
	if !errors.As(err, &challenge) {
		t.Fatalf("second challenge: %v", err)
	}
	session, err := s.CompleteLoginRecoveryCode(ctx, challenge.Token, codes[0])
	if err != nil {
		t.Fatalf("recovery login: %v", err)
	}
	if _, err := s.Authenticate(ctx, session); err != nil {
		t.Fatal(err)
	}
	_, err = s.Login(ctx, name, "password123")
	if !errors.As(err, &challenge) {
		t.Fatal(err)
	}
	if _, err := s.CompleteLoginRecoveryCode(ctx, challenge.Token, codes[0]); !errors.Is(err, ErrTOTPInvalid) {
		t.Fatalf("reused recovery: %v", err)
	}

	if err := s.DisableTOTP(ctx, id, "password123"); err != nil {
		t.Fatal(err)
	}
	token, err = s.Login(ctx, name, "password123")
	if err != nil || token == "" {
		t.Fatalf("login after disable: %q %v", token, err)
	}
}

func TestTOTPReplayAndWindow(t *testing.T) {
	s, now := newTestService(t)
	s.SetTOTPKeys(testTOTPRing(t))
	ctx := context.Background()
	name, id := register(t, s, "")
	enrol, err := s.BeginTOTPEnrolment(ctx, id, name)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrol.Secret)
	code := totpCode(raw, currentTOTPStep(*now))
	if _, err := s.ConfirmTOTPEnrolment(ctx, id, code); err != nil {
		t.Fatal(err)
	}
	_, err = s.Login(ctx, name, "password123")
	var challenge *ChallengeError
	if !errors.As(err, &challenge) {
		t.Fatal(err)
	}
	if _, err := s.CompleteLoginTOTP(ctx, challenge.Token, code); !errors.Is(err, ErrTOTPInvalid) {
		t.Fatalf("replay of enrolment code: %v", err)
	}
}

func TestTOTPKeyRotationDecrypt(t *testing.T) {
	oldKey := bytes.Repeat([]byte{0xaa}, 32)
	newKey := bytes.Repeat([]byte{0xbb}, 32)
	oldRing, err := NewTOTPKeyRing("v1", map[string][]byte{"v1": oldKey})
	if err != nil {
		t.Fatal(err)
	}
	s, now := newTestService(t)
	s.SetTOTPKeys(oldRing)
	ctx := context.Background()
	name, id := register(t, s, "")
	enrol, err := s.BeginTOTPEnrolment(ctx, id, name)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrol.Secret)
	if _, err := s.ConfirmTOTPEnrolment(ctx, id, totpCode(raw, currentTOTPStep(*now))); err != nil {
		t.Fatal(err)
	}
	rotated, err := NewTOTPKeyRing("v2", map[string][]byte{"v1": oldKey, "v2": newKey})
	if err != nil {
		t.Fatal(err)
	}
	s.SetTOTPKeys(rotated)
	*now = now.Add(2 * totpPeriod * time.Second)
	_, err = s.Login(ctx, name, "password123")
	var challenge *ChallengeError
	if !errors.As(err, &challenge) {
		t.Fatal(err)
	}
	token, err := s.CompleteLoginTOTP(ctx, challenge.Token, totpCode(raw, currentTOTPStep(*now)))
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("empty session")
	}
	var keyID string
	if err := s.DB.QueryRow(`SELECT key_id FROM account_totp WHERE account_id = $1`, id).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	if keyID != "v2" {
		t.Fatalf("key_id = %q, want v2 after successful login", keyID)
	}
}

func TestTOTPUnavailableWithoutKeys(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	name, id := register(t, s, "")
	if _, err := s.BeginTOTPEnrolment(ctx, id, name); !errors.Is(err, ErrTOTPUnavailable) {
		t.Fatalf("begin: %v", err)
	}
}

func TestTOTPLoginRehashesWeakPassword(t *testing.T) {
	s, now := newTestService(t)
	s.SetTOTPKeys(testTOTPRing(t))
	ctx := context.Background()
	name, id := register(t, s, "")
	enrol, err := s.BeginTOTPEnrolment(ctx, id, name)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrol.Secret)
	if _, err := s.ConfirmTOTPEnrolment(ctx, id, totpCode(raw, currentTOTPStep(*now))); err != nil {
		t.Fatal(err)
	}
	// Raise service targets after enrolment so the next password step upgrades.
	s.params = Params{MemoryKiB: 16, Iterations: 2, Parallelism: 2}
	var before string
	if err := s.DB.QueryRow(`SELECT password_hash FROM accounts WHERE id = $1`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if !NeedsRehash(before, s.params) {
		t.Fatal("expected stored hash to need rehash")
	}
	_, err = s.Login(ctx, name, "password123")
	var challenge *ChallengeError
	if !errors.As(err, &challenge) {
		t.Fatalf("challenge: %v", err)
	}
	var afterChallenge string
	if err := s.DB.QueryRow(`SELECT password_hash FROM accounts WHERE id = $1`, id).Scan(&afterChallenge); err != nil {
		t.Fatal(err)
	}
	if NeedsRehash(afterChallenge, s.params) {
		t.Fatalf("password step did not rehash: %s", afterChallenge)
	}
	*now = now.Add(totpPeriod * time.Second)
	token, err := s.CompleteLoginTOTP(ctx, challenge.Token, totpCode(raw, currentTOTPStep(*now)))
	if err != nil || token == "" {
		t.Fatalf("complete: %q %v", token, err)
	}
}
