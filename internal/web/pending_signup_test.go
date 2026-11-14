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

func TestDeferredSignupEmailQuotaAcrossIPs(t *testing.T) {
	database := testdb.Open(t)
	service, err := auth.NewService(database)
	if err != nil {
		t.Fatal(err)
	}
	mails := make(chan resetMail, 2)
	handler := New(service, Options{
		Recovery:         &recovery.Config{PublicOrigin: "https://game.example", Sender: testSender{mails}},
		SharedRateLimits: database,
	}).Handler()
	form := url.Values{"username": {"quota1"}, "email": {"quota@example.com"}, "password": {"password123"}}
	first := post(handler, "/signup", form, func(r *http.Request) { r.RemoteAddr = "198.51.100.40:1" })
	if first.Code != http.StatusOK {
		t.Fatalf("first signup = %d %s", first.Code, first.Body)
	}
	select {
	case <-mails:
	case <-time.After(5 * time.Second):
		t.Fatal("expected first signup mail")
	}
	second := post(handler, "/signup", url.Values{
		"username": {"quota2"}, "email": {"QUOTA@example.com"}, "password": {"password123"},
	}, func(r *http.Request) { r.RemoteAddr = "203.0.113.50:1" })
	if second.Code != first.Code || second.Body.String() != first.Body.String() {
		t.Fatalf("quota responses differ: %d %q vs %d %q", first.Code, first.Body, second.Code, second.Body)
	}
	select {
	case <-mails:
		t.Fatal("per-email signup quota allowed a second delivery")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestDeferredSignupInvalidInputDoesNotBurnEmailQuota(t *testing.T) {
	database := testdb.Open(t)
	service, err := auth.NewService(database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Register(t.Context(), "takenuser", "owner@example.com", "password123"); err != nil {
		t.Fatal(err)
	}
	mails := make(chan resetMail, 2)
	handler := New(service, Options{
		Recovery:         &recovery.Config{PublicOrigin: "https://game.example", Sender: testSender{mails}},
		SharedRateLimits: database,
	}).Handler()
	badPassword := post(handler, "/signup", url.Values{
		"username": {"legituser"}, "email": {"victim@example.com"}, "password": {"short"},
	}, func(r *http.Request) { r.RemoteAddr = "198.51.100.60:1" })
	if badPassword.Code != http.StatusBadRequest {
		t.Fatalf("invalid password = %d %s", badPassword.Code, badPassword.Body)
	}
	takenUsername := post(handler, "/signup", url.Values{
		"username": {"takenuser"}, "email": {"victim@example.com"}, "password": {"password123"},
	}, func(r *http.Request) { r.RemoteAddr = "203.0.113.60:1" })
	if takenUsername.Code != http.StatusConflict {
		t.Fatalf("taken username = %d %s", takenUsername.Code, takenUsername.Body)
	}
	legit := post(handler, "/signup", url.Values{
		"username": {"legituser"}, "email": {"victim@example.com"}, "password": {"password123"},
	}, func(r *http.Request) { r.RemoteAddr = "198.51.100.61:1" })
	if legit.Code != http.StatusOK || !strings.Contains(legit.Body.String(), "Check your email") {
		t.Fatalf("legitimate signup after failed attempts = %d %s", legit.Code, legit.Body)
	}
	select {
	case mail := <-mails:
		if mail.to != "victim@example.com" {
			t.Fatalf("unexpected mail: %+v", mail)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("invalid attempts burned the email delivery quota")
	}
}

func TestCompleteSignupDoesNotUseHashSlots(t *testing.T) {
	database := testdb.Open(t)
	service, err := auth.NewService(database)
	if err != nil {
		t.Fatal(err)
	}
	mails := make(chan resetMail, 1)
	server := New(service, Options{
		Recovery: &recovery.Config{PublicOrigin: "https://game.example", Sender: testSender{mails}},
	})
	handler := server.Handler()
	if got := post(handler, "/signup", url.Values{
		"username": {"finish"}, "email": {"finish@example.com"}, "password": {"password123"},
	}, nil).Code; got != http.StatusOK {
		t.Fatalf("signup = %d", got)
	}
	var code string
	select {
	case mail := <-mails:
		code = mail.code
	case <-time.After(5 * time.Second):
		t.Fatal("expected signup mail")
	}
	for i := 0; i < maxConcurrentHashes; i++ {
		if !server.acquire(t.Context()) {
			t.Fatal("failed to fill hash slots")
		}
	}
	t.Cleanup(func() {
		for i := 0; i < maxConcurrentHashes; i++ {
			server.release()
		}
	})
	if got := post(handler, "/login", url.Values{"username": {"finish"}, "password": {"password123"}},
		func(r *http.Request) { r.RemoteAddr = "198.51.100.30:1" }).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("login with full hash slots = %d", got)
	}
	complete := post(handler, "/complete-signup", url.Values{"code": {code}},
		func(r *http.Request) { r.RemoteAddr = "198.51.100.31:1" })
	if complete.Code != http.StatusCreated {
		t.Fatalf("complete signup with full hash slots = %d %s", complete.Code, complete.Body)
	}
}
