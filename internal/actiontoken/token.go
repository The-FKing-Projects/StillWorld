// Package actiontoken issues and verifies signed action request IDs.
package actiontoken

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/actionname"
)

const (
	tokenVersion  = "v1"
	signingDomain = "StillWorld/action-request\x00"
	tokenLifetime = 24 * time.Hour
	nonceSize     = 16
	maxTokenSize  = 512
)

var (
	ErrInvalidConfig = errors.New("action token: invalid key configuration")
	ErrInvalidScope  = errors.New("action token: invalid scope")
	ErrInvalidTime   = errors.New("action token: invalid issuance time")
	ErrInvalidToken  = errors.New("action token: invalid request ID")
	ErrExpiredToken  = errors.New("action token: request ID expired")
)

// Scope binds a request ID to one authenticated player, game, and action.
type Scope struct {
	PlayerID   int64
	GameID     string
	ActionName string
}

// Claims are the authenticated values carried by a request ID.
type Claims struct {
	Scope
	Nonce     [nonceSize]byte
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// KeyRing signs new request IDs with one key and verifies tokens from retained
// keys. A key must remain configured until every token it signed has expired.
type KeyRing struct {
	activeID string
	keys     map[string][]byte
}

// NewKeyRing validates and copies a verification key ring.
func NewKeyRing(activeID string, keys map[string][]byte) (*KeyRing, error) {
	if !actionname.ValidKeyID(activeID) || len(keys) == 0 {
		return nil, ErrInvalidConfig
	}
	copied := make(map[string][]byte, len(keys))
	for id, key := range keys {
		if !actionname.ValidKeyID(id) || len(key) < sha256.Size {
			return nil, ErrInvalidConfig
		}
		copied[id] = append([]byte(nil), key...)
	}
	if _, ok := copied[activeID]; !ok {
		return nil, ErrInvalidConfig
	}
	return &KeyRing{activeID: activeID, keys: copied}, nil
}

// Validate reports whether the key ring is usable for issuance and verification.
func (r *KeyRing) Validate() error {
	if r == nil || !actionname.ValidKeyID(r.activeID) || len(r.keys) == 0 {
		return ErrInvalidConfig
	}
	for id, key := range r.keys {
		if !actionname.ValidKeyID(id) || len(key) < sha256.Size {
			return ErrInvalidConfig
		}
	}
	if _, ok := r.keys[r.activeID]; !ok {
		return ErrInvalidConfig
	}
	return nil
}

// Issue creates a request ID using the supplied database timestamp.
func (r *KeyRing) Issue(scope Scope, issuedAt time.Time) (string, Claims, error) {
	if err := r.Validate(); err != nil {
		return "", Claims{}, ErrInvalidConfig
	}
	key := r.keys[r.activeID]
	if !validScope(scope) {
		return "", Claims{}, ErrInvalidScope
	}
	issuedMicros, err := validIssuedMicros(issuedAt)
	if err != nil {
		return "", Claims{}, err
	}
	if issuedMicros > math.MaxInt64-int64(tokenLifetime/time.Microsecond) {
		return "", Claims{}, ErrInvalidTime
	}

	var nonce [nonceSize]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", Claims{}, fmt.Errorf("generate request nonce: %w", err)
	}
	expiresMicros := issuedMicros + int64(tokenLifetime/time.Microsecond)
	claims := Claims{
		Scope:     scope,
		Nonce:     nonce,
		IssuedAt:  time.UnixMicro(issuedMicros).UTC(),
		ExpiresAt: time.UnixMicro(expiresMicros).UTC(),
	}
	payload := encodeClaims(claims)
	payloadText := base64.RawURLEncoding.EncodeToString(payload)
	signed := tokenVersion + "." + r.activeID + "." + payloadText
	tag := sign(key, signed)
	token := signed + "." + base64.RawURLEncoding.EncodeToString(tag)
	return token, claims, nil
}

// Verify authenticates a request ID and requires its scope to match expected.
// now must be sampled from the database clock by the caller.
func (r *KeyRing) Verify(token string, now time.Time, expected Scope) (Claims, error) {
	if r == nil || len(token) == 0 || len(token) > maxTokenSize || !validScope(expected) {
		return Claims{}, ErrInvalidToken
	}
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != tokenVersion || !actionname.ValidKeyID(parts[1]) {
		return Claims{}, ErrInvalidToken
	}
	key, ok := r.keys[parts[1]]
	if !ok {
		return Claims{}, ErrInvalidToken
	}
	tag, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(tag) != sha256.Size ||
		base64.RawURLEncoding.EncodeToString(tag) != parts[3] {
		return Claims{}, ErrInvalidToken
	}
	signed := parts[0] + "." + parts[1] + "." + parts[2]
	if !hmac.Equal(tag, sign(key, signed)) {
		return Claims{}, ErrInvalidToken
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[2] {
		return Claims{}, ErrInvalidToken
	}
	claims, err := decodeClaims(payload)
	if err != nil || claims.Scope != expected {
		return Claims{}, ErrInvalidToken
	}
	nowMicros, err := validIssuedMicros(now)
	if err != nil || nowMicros < claims.IssuedAt.UnixMicro() {
		return Claims{}, ErrInvalidToken
	}
	if nowMicros >= claims.ExpiresAt.UnixMicro() {
		return Claims{}, ErrExpiredToken
	}
	return claims, nil
}

func encodeClaims(claims Claims) []byte {
	payload := make([]byte, 0,
		nonceSize+8+8+8+1+len(claims.GameID)+1+len(claims.ActionName))
	payload = append(payload, claims.Nonce[:]...)
	payload = binary.BigEndian.AppendUint64(payload, uint64(claims.PlayerID))
	payload = binary.BigEndian.AppendUint64(payload, uint64(claims.IssuedAt.UnixMicro()))
	payload = binary.BigEndian.AppendUint64(payload, uint64(claims.ExpiresAt.UnixMicro()))
	payload = append(payload, byte(len(claims.GameID)))
	payload = append(payload, claims.GameID...)
	payload = append(payload, byte(len(claims.ActionName)))
	payload = append(payload, claims.ActionName...)
	return payload
}

func decodeClaims(payload []byte) (Claims, error) {
	const fixedSize = nonceSize + 8 + 8 + 8 + 1 + 1
	if len(payload) < fixedSize {
		return Claims{}, ErrInvalidToken
	}
	var claims Claims
	position := 0
	copy(claims.Nonce[:], payload[position:position+nonceSize])
	position += nonceSize
	playerID := binary.BigEndian.Uint64(payload[position : position+8])
	position += 8
	issuedMicros := binary.BigEndian.Uint64(payload[position : position+8])
	position += 8
	expiresMicros := binary.BigEndian.Uint64(payload[position : position+8])
	position += 8
	if playerID == 0 || playerID > math.MaxInt64 ||
		issuedMicros > math.MaxInt64 || expiresMicros > math.MaxInt64 ||
		expiresMicros < issuedMicros ||
		expiresMicros-issuedMicros != uint64(tokenLifetime/time.Microsecond) {
		return Claims{}, ErrInvalidToken
	}
	if position >= len(payload) {
		return Claims{}, ErrInvalidToken
	}
	gameLen := int(payload[position])
	position++
	if gameLen == 0 || gameLen > actionname.MaxGameIDLength || gameLen > len(payload)-position {
		return Claims{}, ErrInvalidToken
	}
	claims.GameID = string(payload[position : position+gameLen])
	position += gameLen
	if position >= len(payload) {
		return Claims{}, ErrInvalidToken
	}
	actionLen := int(payload[position])
	position++
	if actionLen == 0 || actionLen > actionname.MaxActionLength || actionLen != len(payload)-position {
		return Claims{}, ErrInvalidToken
	}
	claims.ActionName = string(payload[position:])
	claims.PlayerID = int64(playerID)
	claims.IssuedAt = time.UnixMicro(int64(issuedMicros)).UTC()
	claims.ExpiresAt = time.UnixMicro(int64(expiresMicros)).UTC()
	if !validScope(claims.Scope) {
		return Claims{}, ErrInvalidToken
	}
	return claims, nil
}

func sign(key []byte, token string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(signingDomain))
	_, _ = mac.Write([]byte(token))
	return mac.Sum(nil)
}

func validIssuedMicros(t time.Time) (int64, error) {
	if t.IsZero() {
		return 0, ErrInvalidTime
	}
	micros := t.UTC().UnixMicro()
	if micros < 0 {
		return 0, ErrInvalidTime
	}
	return micros, nil
}

func validScope(scope Scope) bool {
	return scope.PlayerID > 0 &&
		actionname.ValidGameID(scope.GameID) &&
		actionname.ValidActionName(scope.ActionName)
}
