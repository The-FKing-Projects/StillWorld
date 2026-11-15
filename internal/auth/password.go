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

// DefaultParams are the staging single-VPS budget (issue #18 option B):
// about 250–400 ms per hash on a typical small host, 32 MiB per hash, with up
// to four concurrent hash slots in the HTTP layer (~128 MiB peak hashing).
// Re-measure with `stillworld benchmark-password` when retuning.
var DefaultParams = Params{MemoryKiB: 32 * 1024, Iterations: 3, Parallelism: 2}

const (
	saltLen        = 16
	keyLen         = 32
	maxMemoryKiB   = 256 * 1024
	maxIterations  = 10
	maxParallelism = 16
)

var errInvalidHash = errors.New("auth: invalid password hash encoding")

// HashPassword returns a PHC-formatted Argon2id hash of password.
func HashPassword(password string, p Params) (string, error) {
	if !ValidParams(p) {
		return "", errors.New("auth: invalid Argon2id parameters")
	}
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

// ValidParams reports whether p is within the supported Argon2id bounds.
func ValidParams(p Params) bool {
	return p.Parallelism > 0 && p.Parallelism <= maxParallelism &&
		p.Iterations > 0 && p.Iterations <= maxIterations &&
		p.MemoryKiB >= 8*uint32(p.Parallelism) && p.MemoryKiB <= maxMemoryKiB
}

// VerifyPassword reports whether password matches the encoded hash. The cost
// parameters are read from the hash, so old hashes keep working after the
// defaults change.
func VerifyPassword(password, encoded string) (bool, error) {
	p, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.Iterations, p.MemoryKiB, p.Parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// NeedsRehash reports whether encoded uses parameters weaker than current on
// any axis (memory, iterations, or parallelism). When true, callers should
// rehash with UpgradeParams so stronger axes are preserved.
func NeedsRehash(encoded string, current Params) bool {
	p, _, _, err := decodeHash(encoded)
	if err != nil {
		return false
	}
	return p.MemoryKiB < current.MemoryKiB ||
		p.Iterations < current.Iterations ||
		p.Parallelism < current.Parallelism
}

// UpgradeParams returns the per-axis maximum of stored and current so a rehash
// never lowers memory, iterations, or parallelism.
func UpgradeParams(stored, current Params) Params {
	out := Params{
		MemoryKiB:   stored.MemoryKiB,
		Iterations:  stored.Iterations,
		Parallelism: stored.Parallelism,
	}
	if current.MemoryKiB > out.MemoryKiB {
		out.MemoryKiB = current.MemoryKiB
	}
	if current.Iterations > out.Iterations {
		out.Iterations = current.Iterations
	}
	if current.Parallelism > out.Parallelism {
		out.Parallelism = current.Parallelism
	}
	return out
}

func decodeHash(encoded string) (Params, []byte, []byte, error) {
	if len(encoded) > 256 {
		return Params{}, nil, nil, errInvalidHash
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return Params{}, nil, nil, errInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version || parts[2] != fmt.Sprintf("v=%d", version) {
		return Params{}, nil, nil, errInvalidHash
	}
	var p Params
	var par uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Iterations, &par); err != nil || par == 0 || par > 255 {
		return Params{}, nil, nil, errInvalidHash
	}
	p.Parallelism = uint8(par)
	if !ValidParams(p) || parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", p.MemoryKiB, p.Iterations, p.Parallelism) {
		return Params{}, nil, nil, errInvalidHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != saltLen {
		return Params{}, nil, nil, errInvalidHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) != keyLen {
		return Params{}, nil, nil, errInvalidHash
	}
	return p, salt, want, nil
}
