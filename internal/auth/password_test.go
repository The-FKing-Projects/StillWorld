package auth

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

var testParams = Params{MemoryKiB: 8, Iterations: 1, Parallelism: 1}

func TestHashAndVerify(t *testing.T) {
	h, err := HashPassword("correct horse", testParams)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=8,t=1,p=1$") {
		t.Fatalf("unexpected encoding: %s", h)
	}
	if ok, err := VerifyPassword("correct horse", h); err != nil || !ok {
		t.Fatalf("correct password: ok=%v err=%v", ok, err)
	}
	if ok, err := VerifyPassword("wrong", h); err != nil || ok {
		t.Fatalf("wrong password: ok=%v err=%v", ok, err)
	}
}

func TestHashesAreSalted(t *testing.T) {
	a, _ := HashPassword("same", testParams)
	b, _ := HashPassword("same", testParams)
	if a == b {
		t.Fatal("two hashes of the same password should differ")
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	for _, enc := range []string{
		"", "plaintext", "$bcrypt$v=19$m=8,t=1,p=1$AAAA$AAAA",
		"$argon2id$v=19$m=8,t=1,p=0$AAAA$AAAA",
		"$argon2id$v=18$m=8,t=1,p=1$AAAA$AAAA",
		"$argon2id$v=19$m=8,t=1,p=1$!!!$AAAA",
		"$argon2id$v=19$m=8,t=1,p=1$AAAA$",
	} {
		if ok, err := VerifyPassword("x", enc); ok || err == nil {
			t.Errorf("VerifyPassword(%q) = %v, %v; want false, error", enc, ok, err)
		}
	}
}

func TestInvalidArgon2Params(t *testing.T) {
	for _, p := range []Params{
		{MemoryKiB: 8, Iterations: 0, Parallelism: 1},
		{MemoryKiB: 8, Iterations: 1, Parallelism: 0},
		{MemoryKiB: 7, Iterations: 1, Parallelism: 1},
		{MemoryKiB: maxMemoryKiB + 1, Iterations: 1, Parallelism: 1},
		{MemoryKiB: 8, Iterations: maxIterations + 1, Parallelism: 1},
		{MemoryKiB: 256, Iterations: 1, Parallelism: maxParallelism + 1},
	} {
		if _, err := HashPassword("password", p); err == nil {
			t.Errorf("HashPassword accepted %+v", p)
		}
		encoded := fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
			p.MemoryKiB, p.Iterations, p.Parallelism,
			base64.RawStdEncoding.EncodeToString(make([]byte, saltLen)),
			base64.RawStdEncoding.EncodeToString(make([]byte, keyLen)))
		if _, err := VerifyPassword("password", encoded); err == nil {
			t.Errorf("VerifyPassword accepted %+v", p)
		}
	}
}
func TestVerifyUsesStoredParams(t *testing.T) {
	h, _ := HashPassword("pw", Params{MemoryKiB: 16, Iterations: 2, Parallelism: 1})
	if ok, err := VerifyPassword("pw", h); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}
