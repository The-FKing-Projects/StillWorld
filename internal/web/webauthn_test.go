package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/The-FKing-Projects/StillWorld/internal/auth"
	"github.com/The-FKing-Projects/StillWorld/internal/testdb"
	"github.com/go-webauthn/webauthn/webauthn"
)

func TestPasskeyHTTPRegisterBeginAndLoginChallenge(t *testing.T) {
	database := testdb.Open(t)
	service, err := auth.NewService(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetWebAuthn(&auth.WebAuthnConfig{
		RPID: "localhost", RPDisplayName: "Test", RPOrigins: []string{"http://localhost"},
	}); err != nil {
		t.Fatal(err)
	}
	handler := New(service, Options{InsecureCookies: true}).Handler()

	if w := post(handler, "/signup", url.Values{
		"username": {"passkeyuser"}, "password": {"password123"},
	}, nil); w.Code != http.StatusCreated {
		t.Fatalf("signup = %d %s", w.Code, w.Body)
	}
	session := sessionCookie(t, post(handler, "/login", url.Values{
		"username": {"passkeyuser"}, "password": {"password123"},
	}, nil))

	begin := post(handler, "/passkeys/begin", url.Values{}, func(r *http.Request) {
		r.AddCookie(session)
	})
	if begin.Code != http.StatusOK || !strings.Contains(begin.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("begin = %d %s %s", begin.Code, begin.Header().Get("Content-Type"), begin.Body)
	}
	if !json.Valid(begin.Body.Bytes()) {
		t.Fatalf("invalid json: %s", begin.Body)
	}

	finish := post(handler, "/passkeys/finish", url.Values{
		"credential": {"{}"}, "label": {"phone"},
	}, func(r *http.Request) {
		r.AddCookie(session)
	})
	if finish.Code != http.StatusBadRequest {
		t.Fatalf("finish bad body = %d %s", finish.Code, finish.Body)
	}

	cred := webauthn.Credential{ID: []byte{9, 8, 7}, PublicKey: []byte{3}}
	raw, err := json.Marshal(cred)
	if err != nil {
		t.Fatal(err)
	}
	acct, err := service.Authenticate(t.Context(), session.Value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO webauthn_credentials
		(account_id, credential_id, credential_json) VALUES ($1, $2, $3)`,
		acct.ID, cred.ID, raw); err != nil {
		t.Fatal(err)
	}

	login := post(handler, "/login", url.Values{
		"username": {"passkeyuser"}, "password": {"password123"},
	}, nil)
	if login.Code != http.StatusUnauthorized || !strings.Contains(login.Body.String(), "second factor required") {
		t.Fatalf("login = %d %s", login.Code, login.Body)
	}
	parts := strings.Split(strings.TrimSpace(login.Body.String()), "\n")
	if len(parts) < 2 {
		t.Fatalf("challenge body: %q", login.Body)
	}
	challenge := strings.TrimSpace(parts[1])
	beginLogin := post(handler, "/login/passkey/begin", url.Values{
		"challenge": {challenge},
	}, nil)
	if beginLogin.Code != http.StatusOK || !json.Valid(beginLogin.Body.Bytes()) {
		t.Fatalf("passkey begin = %d %s", beginLogin.Code, beginLogin.Body)
	}
}
