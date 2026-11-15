package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/The-FKing-Projects/StillWorld/internal/actionname"
)

var (
	// ErrTOTPUnavailable means TOTP keys are not configured.
	ErrTOTPUnavailable = errors.New("auth: totp unavailable")
	// ErrSecondFactorRequired means password was accepted and a second factor is needed.
	ErrSecondFactorRequired = errors.New("auth: second factor required")
	// ErrTOTPRequired is retained as an alias of ErrSecondFactorRequired.
	ErrTOTPRequired = ErrSecondFactorRequired
	// ErrTOTPInvalid means the code or recovery code was rejected.
	ErrTOTPInvalid = errors.New("auth: invalid totp code")
	// ErrTOTPNotEnabled means the account has no confirmed TOTP.
	ErrTOTPNotEnabled = errors.New("auth: totp not enabled")
	// ErrTOTPAlreadyEnabled means enrolment was attempted while TOTP is active.
	ErrTOTPAlreadyEnabled = errors.New("auth: totp already enabled")
	// ErrTOTPChallenge means the login challenge is missing or expired.
	ErrTOTPChallenge = errors.New("auth: invalid or expired login challenge")
)

// ChallengeError is returned from Login when a second factor is required. Unwrap
// to ErrSecondFactorRequired; Token is the opaque challenge for CompleteLoginTOTP
// / CompleteLoginPasskey.
type ChallengeError struct {
	Token string
}

func (e *ChallengeError) Error() string { return ErrSecondFactorRequired.Error() }
func (e *ChallengeError) Unwrap() error { return ErrSecondFactorRequired }

// TOTPKeyRing holds AEAD keys for encrypting TOTP secrets at rest.
type TOTPKeyRing struct {
	activeID string
	keys     map[string][]byte
	gcm      map[string]cipher.AEAD
}

// NewTOTPKeyRing validates and copies a TOTP encryption key ring. Each key must
// be 32 bytes (AES-256). Active ID must be present.
func NewTOTPKeyRing(activeID string, keys map[string][]byte) (*TOTPKeyRing, error) {
	if !actionname.ValidKeyID(activeID) || len(keys) == 0 {
		return nil, ErrTOTPUnavailable
	}
	copied := make(map[string][]byte, len(keys))
	gcms := make(map[string]cipher.AEAD, len(keys))
	for id, key := range keys {
		if !actionname.ValidKeyID(id) || len(key) != 32 {
			return nil, fmt.Errorf("%w: key %q must be 32 bytes", ErrTOTPUnavailable, id)
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrTOTPUnavailable, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrTOTPUnavailable, err)
		}
		copied[id] = append([]byte(nil), key...)
		gcms[id] = aead
	}
	if _, ok := copied[activeID]; !ok {
		return nil, ErrTOTPUnavailable
	}
	return &TOTPKeyRing{activeID: activeID, keys: copied, gcm: gcms}, nil
}

func (r *TOTPKeyRing) encrypt(plaintext []byte) (keyID string, nonce, ciphertext []byte, err error) {
	if r == nil {
		return "", nil, nil, ErrTOTPUnavailable
	}
	aead := r.gcm[r.activeID]
	nonce = make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", nil, nil, fmt.Errorf("generate totp nonce: %w", err)
	}
	return r.activeID, nonce, aead.Seal(nil, nonce, plaintext, nil), nil
}

func (r *TOTPKeyRing) decrypt(keyID string, nonce, ciphertext []byte) ([]byte, error) {
	if r == nil {
		return nil, ErrTOTPUnavailable
	}
	aead, ok := r.gcm[keyID]
	if !ok {
		return nil, ErrTOTPUnavailable
	}
	if len(nonce) != aead.NonceSize() {
		return nil, ErrTOTPUnavailable
	}
	plain, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrTOTPInvalid
	}
	return plain, nil
}
