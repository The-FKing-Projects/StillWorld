package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/auth"
	"github.com/The-FKing-Projects/StillWorld/internal/testdb"
	"github.com/The-FKing-Projects/StillWorld/recovery"
)

func TestDeferredSignupHidesEmailConflict(t *testing.T) {
	database := testdb.Open(t)
	service, err := auth.NewService(database)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if _, err := service.Register(ctx, "owner", "taken@example.com", "password123"); err != nil {
		t.Fatal(err)
	}
	mails := make(chan resetMail, 2)
	handler := New(service, Options{
		Recovery: &recovery.Config{PublicOrigin: "https://game.example", Sender: testSender{mails}},
	}).Handler()

	existing := post(handler, "/signup", url.Values{
		"username": {"intruder"}, "email": {"taken@example.com"}, "password": {"password123"},
	}, nil)
	fresh := post(handler, "/signup", url.Values{
		"username": {"newbie"}, "email": {"new@example.com"}, "password": {"password123"},
	}, func(r *http.Request) { r.RemoteAddr = "198.51.100.20:1" })
	if existing.Code != fresh.Code || existing.Body.String() != fresh.Body.String() {
		t.Fatalf("responses differ: %d %q vs %d %q", existing.Code, existing.Body, fresh.Code, fresh.Body)
	}
	if existing.Code != http.StatusOK || !strings.Contains(existing.Body.String(), "Check your email") {
		t.Fatalf("generic signup response = %d %s", existing.Code, existing.Body)
	}
	select {
	case mail := <-mails:
		if mail.to != "new@example.com" || mail.url != "https://game.example/complete-signup" {
			t.Fatalf("unexpected mail: %+v", mail)
		}
		complete := post(handler, "/complete-signup", url.Values{"code": {mail.code}},
			func(r *http.Request) { r.RemoteAddr = "198.51.100.21:1" })
		if complete.Code != http.StatusCreated {
			t.Fatalf("complete signup = %d %s", complete.Code, complete.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("expected signup mail for new address")
	}
	select {
	case <-mails:
		t.Fatal("existing address received a signup code")
	case <-time.After(200 * time.Millisecond):
	}
	if _, err := service.Login(ctx, "intruder", "password123"); err == nil {
		t.Fatal("existing-email signup created a loginable account")
	}
	if _, err := service.Login(ctx, "newbie", "password123"); err != nil {
		t.Fatalf("completed signup login: %v", err)
	}
}
