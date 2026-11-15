package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	totpDigits     = 6
	totpPeriod     = 30
	totpSecretSize = 20
	totpWindow     = 1 // accept current step ±1
	totpIssuer     = "StillWorld"

	challengeTTL      = 5 * time.Minute
	pendingEnrolTTL   = 10 * time.Minute
	recoveryCodeCount = 10
	recoveryCodeBytes = 5 // 8 base32 chars
)

func generateTOTPSecret() (raw []byte, base32Secret string, err error) {
	raw = make([]byte, totpSecretSize)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", fmt.Errorf("generate totp secret: %w", err)
	}
	return raw, base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

func otpauthURL(username, base32Secret string) string {
	label := url.PathEscape(totpIssuer + ":" + username)
	q := url.Values{}
	q.Set("secret", base32Secret)
	q.Set("issuer", totpIssuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprintf("%d", totpDigits))
	q.Set("period", fmt.Sprintf("%d", totpPeriod))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

func totpCode(secret []byte, step int64) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	_, _ = mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	truncated := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	code := truncated % 1_000_000
	return fmt.Sprintf("%06d", code)
}

func currentTOTPStep(now time.Time) int64 {
	return now.Unix() / totpPeriod
}

// verifyTOTPCode reports whether code matches secret within the acceptance
// window and has a step strictly greater than lastStep (replay protection).
// On success it returns the accepted step to store as the new lastStep.
func verifyTOTPCode(secret []byte, code string, now time.Time, lastStep int64) (step int64, ok bool) {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return 0, false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	base := currentTOTPStep(now)
	var best int64 = -1
	for delta := -totpWindow; delta <= totpWindow; delta++ {
		step := base + int64(delta)
		if step <= lastStep {
			continue
		}
		if totpCode(secret, step) == code {
			if step > best {
				best = step
			}
		}
	}
	if best < 0 {
		return 0, false
	}
	return best, true
}

func generateRecoveryCodes() (plain []string, hashes [][]byte, err error) {
	plain = make([]string, recoveryCodeCount)
	hashes = make([][]byte, recoveryCodeCount)
	for i := 0; i < recoveryCodeCount; i++ {
		raw := make([]byte, recoveryCodeBytes)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, fmt.Errorf("generate recovery code: %w", err)
		}
		code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
		plain[i] = code
		hashes[i] = hashToken(strings.ToUpper(code))
	}
	return plain, hashes, nil
}
