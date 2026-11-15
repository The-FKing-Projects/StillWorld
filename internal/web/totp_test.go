package web

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/auth"
	"github.com/The-FKing-Projects/StillWorld/internal/testdb"
)

func TestTOTPHTTPLoginFlow(t *testing.T) {
	database := testdb.Open(t)
	service, err := auth.NewService(database)
	if err != nil {
		t.Fatal(err)
	}
	ring, err := auth.NewTOTPKeyRing("v1", map[string][]byte{"v1": bytes.Repeat([]byte{0x33}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	service.SetTOTPKeys(ring)
	now := time.Now().UTC().Truncate(time.Second)
	service.Now = func() time.Time { return now }
	handler := New(service, Options{InsecureCookies: true}).Handler()

	if w := post(handler, "/signup", url.Values{
		"username": {"totpuser"}, "password": {"password123"},
	}, nil); w.Code != http.StatusCreated {
		t.Fatalf("signup = %d %s", w.Code, w.Body)
	}
	session := sessionCookie(t, post(handler, "/login", url.Values{
		"username": {"totpuser"}, "password": {"password123"},
	}, nil))

	begin := post(handler, "/totp/begin", url.Values{}, func(r *http.Request) {
		r.AddCookie(session)
	})
	if begin.Code != http.StatusOK {
		t.Fatalf("begin = %d %s", begin.Code, begin.Body)
	}
	secretLine := ""
	for _, line := range strings.Split(begin.Body.String(), "\n") {
		if strings.HasPrefix(line, "secret ") {
			secretLine = strings.TrimPrefix(line, "secret ")
		}
	}
	if secretLine == "" {
		t.Fatalf("missing secret in %q", begin.Body)
	}
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretLine)
	if err != nil {
		t.Fatal(err)
	}
	code := rfc6238(raw, now.Unix()/30)
	confirm := post(handler, "/totp/confirm", url.Values{"code": {code}}, func(r *http.Request) {
		r.AddCookie(session)
	})
	if confirm.Code != http.StatusCreated {
		t.Fatalf("confirm = %d %s", confirm.Code, confirm.Body)
	}
	recoveryLines := strings.Split(strings.TrimSpace(confirm.Body.String()), "\n")
	if len(recoveryLines) < 2 {
		t.Fatalf("recovery codes: %q", confirm.Body)
	}
	recoveryCode := recoveryLines[1]

	login := post(handler, "/login", url.Values{
		"username": {"totpuser"}, "password": {"password123"},
	}, nil)
	if login.Code != http.StatusUnauthorized || !strings.Contains(login.Body.String(), "totp required") {
		t.Fatalf("login = %d %s", login.Code, login.Body)
	}
	challenge := ""
	for _, line := range strings.Split(login.Body.String(), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && line != "totp required" {
			challenge = line
			break
		}
	}
	if challenge == "" {
		t.Fatal("missing challenge token")
	}
	now = now.Add(30 * time.Second)
	service.Now = func() time.Time { return now }
	totpLogin := post(handler, "/login/totp", url.Values{
		"challenge": {challenge}, "code": {rfc6238(raw, now.Unix()/30)},
	}, nil)
	if totpLogin.Code != http.StatusOK {
		t.Fatalf("totp login = %d %s", totpLogin.Code, totpLogin.Body)
	}
	_ = sessionCookie(t, totpLogin)

	login = post(handler, "/login", url.Values{
		"username": {"totpuser"}, "password": {"password123"},
	}, nil)
	parts := strings.Split(strings.TrimSpace(login.Body.String()), "\n")
	if len(parts) < 2 {
		t.Fatalf("challenge body: %q", login.Body)
	}
	challenge = strings.TrimSpace(parts[1])
	recLogin := post(handler, "/login/recovery-code", url.Values{
		"challenge": {challenge}, "code": {recoveryCode},
	}, nil)
	if recLogin.Code != http.StatusOK {
		t.Fatalf("recovery login = %d %s", recLogin.Code, recLogin.Body)
	}
}

func rfc6238(secret []byte, step int64) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	_, _ = mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	truncated := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", truncated%1_000_000)
}
