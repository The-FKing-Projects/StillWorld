// Package auth implements accounts, password hashing, and server-side sessions.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are the Argon2id cost parameters.
type Params struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
}

// DefaultParams are conservative starting values; tune them to the host.
var DefaultParams = Params{MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 2}

const (
	saltLen = 16
	keyLen  = 32
)

var errInvalidHash = errors.New("auth: invalid password hash encoding")

// HashPassword returns a PHC-formatted Argon2id hash of password.
func HashPassword(password string, p Params) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.Iterations, p.MemoryKiB, p.Parallelism, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the encoded hash. The cost
// parameters are read from the hash, so old hashes keep working after the
// defaults change.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errInvalidHash
	}
	var p Params
	var par uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Iterations, &par); err != nil || par == 0 || par > 255 {
		return false, errInvalidHash
	}
	p.Parallelism = uint8(par)
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errInvalidHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errInvalidHash
	}
	got := argon2.IDKey([]byte(password), salt, p.Iterations, p.MemoryKiB, p.Parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
