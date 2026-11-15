package auth

import (
	"context"
	"encoding/base32"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

func testWebAuthn(t *testing.T, s *Service) {
	t.Helper()
	if err := s.SetWebAuthn(&WebAuthnConfig{
		RPID:          "localhost",
		RPDisplayName: "StillWorld Test",
		RPOrigins:     []string{"http://localhost:8080"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPasskeyLoginChallengeWithoutCeremony(t *testing.T) {
	s, _ := newTestService(t)
	testWebAuthn(t, s)
	ctx := context.Background()
	name, id := register(t, s, "")

	// Insert a stand-in public credential so Login requires a second factor.
	cred := webauthn.Credential{ID: []byte{1, 2, 3, 4}, PublicKey: []byte{9, 9, 9}}
	raw, err := json.Marshal(cred)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO webauthn_credentials
		(account_id, credential_id, credential_json, label) VALUES ($1, $2, $3, $4)`,
		id, cred.ID, raw, "test"); err != nil {
		t.Fatal(err)
	}

	_, err = s.Login(ctx, name, "password123")
	var challenge *ChallengeError
	if !errors.As(err, &challenge) || challenge.Token == "" {
		t.Fatalf("login challenge: %v", err)
	}
	info, err := s.LoginChallengeFactors(ctx, challenge.Token)
	if err != nil || info.TOTP || !info.Passkey {
		t.Fatalf("factors: %#v %v", info, err)
	}
	options, err := s.BeginPasskeyLogin(ctx, challenge.Token)
	if err != nil || len(options) == 0 {
		t.Fatalf("begin login: %v %s", err, options)
	}
	if _, err := s.CompleteLoginPasskey(ctx, challenge.Token, []byte(`{}`)); !errors.Is(err, ErrWebAuthnInvalid) {
		t.Fatalf("bad assertion: %v", err)
	}
}

func TestPasskeyRegistrationBeginAndDelete(t *testing.T) {
	s, _ := newTestService(t)
	testWebAuthn(t, s)
	ctx := context.Background()
	name, id := register(t, s, "")

	options, err := s.BeginPasskeyRegistration(ctx, id, name)
	if err != nil || len(options) == 0 {
		t.Fatalf("begin register: %v %s", err, options)
	}
	if err := s.FinishPasskeyRegistration(ctx, id, name, []byte(`{}`), "laptop"); !errors.Is(err, ErrWebAuthnInvalid) {
		t.Fatalf("bad attestation: %v", err)
	}

	cred := webauthn.Credential{ID: []byte{7, 7, 7}, PublicKey: []byte{1}}
	raw, err := json.Marshal(cred)
	if err != nil {
		t.Fatal(err)
	}
	var rowID int64
	if err := s.DB.QueryRowContext(ctx, `INSERT INTO webauthn_credentials
		(account_id, credential_id, credential_json, label) VALUES ($1, $2, $3, $4) RETURNING id`,
		id, cred.ID, raw, "laptop").Scan(&rowID); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListPasskeys(ctx, id)
	if err != nil || len(list) != 1 || list[0].Label != "laptop" {
		t.Fatalf("list: %#v %v", list, err)
	}
	if err := s.DeletePasskey(ctx, id, rowID, "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("bad password: %v", err)
	}
	if err := s.DeletePasskey(ctx, id, rowID, "password123"); err != nil {
		t.Fatal(err)
	}
	list, err = s.ListPasskeys(ctx, id)
	if err != nil || len(list) != 0 {
		t.Fatalf("after delete: %#v %v", list, err)
	}
}

func TestEitherFactorChallengeWithTOTPAndPasskey(t *testing.T) {
	s, now := newTestService(t)
	s.SetTOTPKeys(testTOTPRing(t))
	testWebAuthn(t, s)
	ctx := context.Background()
	name, id := register(t, s, "")

	enrol, err := s.BeginTOTPEnrolment(ctx, id, name)
	if err != nil {
		t.Fatal(err)
	}
	rawSecret, err := decodeTOTPSecret(enrol.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmTOTPEnrolment(ctx, id, totpCode(rawSecret, currentTOTPStep(*now))); err != nil {
		t.Fatal(err)
	}
	cred := webauthn.Credential{ID: []byte{5, 5}, PublicKey: []byte{2}}
	raw, err := json.Marshal(cred)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO webauthn_credentials
		(account_id, credential_id, credential_json) VALUES ($1, $2, $3)`,
		id, cred.ID, raw); err != nil {
		t.Fatal(err)
	}

	_, err = s.Login(ctx, name, "password123")
	var challenge *ChallengeError
	if !errors.As(err, &challenge) {
		t.Fatalf("challenge: %v", err)
	}
	info, err := s.LoginChallengeFactors(ctx, challenge.Token)
	if err != nil || !info.TOTP || !info.Passkey {
		t.Fatalf("factors: %#v %v", info, err)
	}
	*now = now.Add(totpPeriod * time.Second)
	token, err := s.CompleteLoginTOTP(ctx, challenge.Token, totpCode(rawSecret, currentTOTPStep(*now)))
	if err != nil {
		t.Fatalf("totp complete with passkey also enrolled: %v", err)
	}
	if _, err := s.Authenticate(ctx, token); err != nil {
		t.Fatal(err)
	}
}

func TestWebAuthnUnavailableWithoutConfig(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	name, id := register(t, s, "")
	if _, err := s.BeginPasskeyRegistration(ctx, id, name); !errors.Is(err, ErrWebAuthnUnavailable) {
		t.Fatalf("begin: %v", err)
	}
}

func decodeTOTPSecret(secret string) ([]byte, error) {
	return base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
}
