package actiontoken

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func testKeyRing(t *testing.T) *KeyRing {
	t.Helper()
	ring, err := NewKeyRing("current", map[string][]byte{
		"current": bytes.Repeat([]byte{0x42}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

func testScope() Scope {
	return Scope{PlayerID: 42, GameID: "counter_game", ActionName: "counter.increment"}
}

func TestIssueAndVerify(t *testing.T) {
	ring := testKeyRing(t)
	issued := time.Date(2026, 11, 13, 6, 0, 0, 123456000, time.UTC)

	token, want, err := ring.Issue(testScope(), issued)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ring.Verify(token, issued.Add(time.Second), testScope())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("claims = %#v, want %#v", got, want)
	}
	if got.ExpiresAt.Sub(got.IssuedAt) != tokenLifetime {
		t.Fatalf("token lifetime = %s, want %s", got.ExpiresAt.Sub(got.IssuedAt), tokenLifetime)
	}
}

func TestVerifyRejectsTamperingAndScopeMismatch(t *testing.T) {
	ring := testKeyRing(t)
	issued := time.Date(2026, 11, 13, 6, 0, 0, 0, time.UTC)
	token, _, err := ring.Issue(testScope(), issued)
	if err != nil {
		t.Fatal(err)
	}

	tampered := []byte(token)
	tampered[len(tampered)-1] ^= 1
	if _, err := ring.Verify(string(tampered), issued, testScope()); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("tampered token error = %v, want ErrInvalidToken", err)
	}

	otherScope := testScope()
	otherScope.PlayerID++
	if _, err := ring.Verify(token, issued, otherScope); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("scope mismatch error = %v, want ErrInvalidToken", err)
	}
}

func TestVerifyExpiryBoundaryAndFutureIssuance(t *testing.T) {
	ring := testKeyRing(t)
	issued := time.Date(2026, 11, 13, 6, 0, 0, 0, time.UTC)
	token, claims, err := ring.Issue(testScope(), issued)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ring.Verify(token, claims.ExpiresAt.Add(-time.Microsecond), testScope()); err != nil {
		t.Fatalf("token immediately before expiry rejected: %v", err)
	}
	if _, err := ring.Verify(token, claims.ExpiresAt, testScope()); !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("expiry-boundary error = %v, want ErrExpiredToken", err)
	}
	if _, err := ring.Verify(token, issued.Add(-time.Microsecond), testScope()); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("future issuance error = %v, want ErrInvalidToken", err)
	}
}

func TestVerifySupportsRetainedRotationKeys(t *testing.T) {
	oldKey := bytes.Repeat([]byte{0x11}, 32)
	oldRing, err := NewKeyRing("old", map[string][]byte{"old": oldKey})
	if err != nil {
		t.Fatal(err)
	}
	issued := time.Date(2026, 11, 13, 6, 0, 0, 0, time.UTC)
	token, _, err := oldRing.Issue(testScope(), issued)
	if err != nil {
		t.Fatal(err)
	}

	rotated, err := NewKeyRing("new", map[string][]byte{
		"old": oldKey,
		"new": bytes.Repeat([]byte{0x22}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rotated.Verify(token, issued.Add(time.Hour), testScope()); err != nil {
		t.Fatalf("retained key token rejected after rotation: %v", err)
	}
}

func TestNewKeyRingRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		active string
		keys   map[string][]byte
	}{
		{name: "missing active key", active: "active", keys: map[string][]byte{"old": bytes.Repeat([]byte{1}, 32)}},
		{name: "short key", active: "active", keys: map[string][]byte{"active": bytes.Repeat([]byte{1}, 31)}},
		{name: "invalid key ID", active: "bad.id", keys: map[string][]byte{"bad.id": bytes.Repeat([]byte{1}, 32)}},
		{name: "empty key ring", active: "active", keys: map[string][]byte{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewKeyRing(tt.active, tt.keys); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("NewKeyRing error = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestKeyRingCopiesKeyMaterial(t *testing.T) {
	key := bytes.Repeat([]byte{0x31}, 32)
	original := append([]byte(nil), key...)
	ring, err := NewKeyRing("current", map[string][]byte{"current": key})
	if err != nil {
		t.Fatal(err)
	}
	key[0] ^= 0xff

	issued := time.Date(2026, 11, 13, 6, 0, 0, 0, time.UTC)
	token, _, err := ring.Issue(testScope(), issued)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewKeyRing("current", map[string][]byte{"current": original})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(token, issued, testScope()); err != nil {
		t.Fatalf("token signed after caller key mutation was rejected: %v", err)
	}
}

func TestIssueRejectsInvalidScopeAndTime(t *testing.T) {
	ring := testKeyRing(t)
	issued := time.Date(2026, 11, 13, 6, 0, 0, 0, time.UTC)
	for _, scope := range []Scope{
		{PlayerID: 0, GameID: "counter", ActionName: "counter.increment"},
		{PlayerID: 42, GameID: "Counter", ActionName: "counter.increment"},
		{PlayerID: 42, GameID: "counter", ActionName: "counter..increment"},
		{PlayerID: 42, GameID: "counter", ActionName: "counter.increment/other"},
	} {
		if _, _, err := ring.Issue(scope, issued); !errors.Is(err, ErrInvalidScope) {
			t.Errorf("Issue(%#v) error = %v, want ErrInvalidScope", scope, err)
		}
	}
	if _, _, err := ring.Issue(testScope(), time.Time{}); !errors.Is(err, ErrInvalidTime) {
		t.Fatalf("zero time error = %v, want ErrInvalidTime", err)
	}
	if _, _, err := ring.Issue(testScope(), time.UnixMicro(-1)); !errors.Is(err, ErrInvalidTime) {
		t.Fatalf("negative time error = %v, want ErrInvalidTime", err)
	}
	if _, _, err := (*KeyRing)(nil).Issue(testScope(), issued); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil key ring error = %v, want ErrInvalidConfig", err)
	}
}

func TestVerifyRejectsNonCanonicalToken(t *testing.T) {
	ring := testKeyRing(t)
	issued := time.Date(2026, 11, 13, 6, 0, 0, 0, time.UTC)
	token, _, err := ring.Issue(testScope(), issued)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(token, "=") {
		t.Fatal("issued token unexpectedly contains base64 padding")
	}
	if _, err := ring.Verify(token+".", issued, testScope()); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("non-canonical token error = %v, want ErrInvalidToken", err)
	}
}
