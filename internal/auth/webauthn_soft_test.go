package auth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/big"
	"testing"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
)

const testRPOrigin = "http://localhost:8080"

// softAuthenticator is a minimal ES256 authenticator for ceremony round-trip tests.
type softAuthenticator struct {
	key          *ecdsa.PrivateKey
	credentialID []byte
	signCount    uint32
}

func newSoftAuthenticator(t *testing.T) *softAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	return &softAuthenticator{key: key, credentialID: id}
}

func (a *softAuthenticator) create(t *testing.T, optionsJSON []byte) []byte {
	t.Helper()
	var creation protocol.CredentialCreation
	if err := json.Unmarshal(optionsJSON, &creation); err != nil {
		t.Fatal(err)
	}
	challenge := []byte(creation.Response.Challenge)
	clientData := map[string]any{
		"type":      "webauthn.create",
		"challenge": base64.RawURLEncoding.EncodeToString(challenge),
		"origin":    testRPOrigin,
	}
	clientDataJSON, err := json.Marshal(clientData)
	if err != nil {
		t.Fatal(err)
	}
	pub := webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{
			KeyType:   int64(webauthncose.EllipticKey),
			Algorithm: int64(webauthncose.AlgES256),
		},
		Curve:  int64(webauthncose.P256),
		XCoord: padCoord(a.key.PublicKey.X),
		YCoord: padCoord(a.key.PublicKey.Y),
	}
	coseKey, err := webauthncbor.Marshal(pub)
	if err != nil {
		t.Fatal(err)
	}
	flags := protocol.FlagUserPresent | protocol.FlagAttestedCredentialData
	authData := buildAuthData(t, creation.Response.RelyingParty.ID, flags, a.signCount, a.credentialID, coseKey)
	attObj, err := webauthncbor.Marshal(map[string]any{
		"fmt":      "none",
		"attStmt":  map[string]any{},
		"authData": authData,
	})
	if err != nil {
		t.Fatal(err)
	}
	id := base64.RawURLEncoding.EncodeToString(a.credentialID)
	body, err := json.Marshal(map[string]any{
		"id":    id,
		"rawId": id,
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientDataJSON),
			"attestationObject": base64.RawURLEncoding.EncodeToString(attObj),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func (a *softAuthenticator) assert(t *testing.T, optionsJSON []byte, rpID string) []byte {
	t.Helper()
	var assertion protocol.CredentialAssertion
	if err := json.Unmarshal(optionsJSON, &assertion); err != nil {
		t.Fatal(err)
	}
	challenge := []byte(assertion.Response.Challenge)
	clientData := map[string]any{
		"type":      "webauthn.get",
		"challenge": base64.RawURLEncoding.EncodeToString(challenge),
		"origin":    testRPOrigin,
	}
	clientDataJSON, err := json.Marshal(clientData)
	if err != nil {
		t.Fatal(err)
	}
	a.signCount++
	flags := protocol.FlagUserPresent
	authData := buildAuthData(t, rpID, flags, a.signCount, nil, nil)
	clientHash := sha256.Sum256(clientDataJSON)
	sigPayload := append(append([]byte{}, authData...), clientHash[:]...)
	digest := sha256.Sum256(sigPayload)
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	id := base64.RawURLEncoding.EncodeToString(a.credentialID)
	body, err := json.Marshal(map[string]any{
		"id":    id,
		"rawId": id,
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientDataJSON),
			"authenticatorData": base64.RawURLEncoding.EncodeToString(authData),
			"signature":         base64.RawURLEncoding.EncodeToString(sig),
			"userHandle":        "",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func buildAuthData(t *testing.T, rpID string, flags protocol.AuthenticatorFlags, counter uint32, credID, coseKey []byte) []byte {
	t.Helper()
	rpHash := sha256.Sum256([]byte(rpID))
	var buf bytes.Buffer
	buf.Write(rpHash[:])
	buf.WriteByte(byte(flags))
	_ = binary.Write(&buf, binary.BigEndian, counter)
	if flags.HasAttestedCredentialData() {
		buf.Write(make([]byte, 16)) // AAGUID
		_ = binary.Write(&buf, binary.BigEndian, uint16(len(credID)))
		buf.Write(credID)
		buf.Write(coseKey)
	}
	return buf.Bytes()
}

func padCoord(n *big.Int) []byte {
	b := n.Bytes()
	if len(b) >= 32 {
		return b[len(b)-32:]
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

func TestPasskeyRegisterAndLoginRoundTrip(t *testing.T) {
	s, _ := newTestService(t)
	testWebAuthn(t, s)
	ctx := context.Background()
	name, id := register(t, s, "")
	authn := newSoftAuthenticator(t)

	options, err := s.BeginPasskeyRegistration(ctx, id, name)
	if err != nil {
		t.Fatal(err)
	}
	attestation := authn.create(t, options)
	if err := s.FinishPasskeyRegistration(ctx, id, name, attestation, "soft"); err != nil {
		t.Fatalf("finish registration: %v", err)
	}
	list, err := s.ListPasskeys(ctx, id)
	if err != nil || len(list) != 1 || list[0].Label != "soft" {
		t.Fatalf("list after register: %#v %v", list, err)
	}

	_, err = s.Login(ctx, name, "password123")
	var challenge *ChallengeError
	if !errors.As(err, &challenge) {
		t.Fatalf("expected challenge: %v", err)
	}
	assertOpts, err := s.BeginPasskeyLogin(ctx, challenge.Token)
	if err != nil {
		t.Fatal(err)
	}
	assertion := authn.assert(t, assertOpts, "localhost")
	token, err := s.CompleteLoginPasskey(ctx, challenge.Token, assertion)
	if err != nil {
		t.Fatalf("complete passkey login: %v", err)
	}
	if _, err := s.Authenticate(ctx, token); err != nil {
		t.Fatal(err)
	}

	// Sign counter must advance on the stored credential.
	var raw []byte
	if err := s.DB.QueryRowContext(ctx, `SELECT credential_json FROM webauthn_credentials WHERE account_id = $1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Authenticator struct {
			SignCount uint32 `json:"signCount"`
		} `json:"authenticator"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Authenticator.SignCount != authn.signCount {
		t.Fatalf("sign count = %d, want %d", stored.Authenticator.SignCount, authn.signCount)
	}
}
