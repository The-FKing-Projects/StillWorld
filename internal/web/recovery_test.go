package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/auth"
	"github.com/The-FKing-Projects/StillWorld/internal/testdb"
	"github.com/The-FKing-Projects/StillWorld/recovery"
)

type resetMail struct {
	to, code, url string
}

type testSender struct{ messages chan resetMail }

func (s testSender) SendReset(_ context.Context, to, code, resetURL string) error {
	s.messages <- resetMail{to, code, resetURL}
	return nil
}

type recoveryStub struct {
	*fakeAuth
	calls chan string
}

func (s recoveryStub) IssueResetTokenForDelivery(_ context.Context, email string) (string, string, error) {
	s.calls <- email
	if email == "known@example.com" {
		return "test-code", email, nil
	}
	return "", "", auth.ErrRecoveryUnavailable
}

func (s recoveryStub) RedeemResetToken(context.Context, string, string) error {
	return auth.ErrInvalidResetToken
}

func TestRecoveryResponsesAndLimits(t *testing.T) {
	authenticator := recoveryStub{fakeAuth: newFakeAuth(), calls: make(chan string, 20)}
	cfg := &recovery.Config{PublicOrigin: "https://game.example", Sender: testSender{make(chan resetMail, 1)}}
	server := New(authenticator, Options{Recovery: cfg, Actions: &fakeActionService{}, Presentation: testPresentation{}})
	handler := server.Handler()
	known := post(handler, "/forgot-password", url.Values{"email": {"known@example.com"}}, nil)
	unknown := post(handler, "/forgot-password", url.Values{"email": {"unknown@example.com"}}, nil)
	if known.Code != http.StatusOK || known.Code != unknown.Code || known.Body.String() != unknown.Body.String() {
		t.Fatal("request responses differ")
	}
	for range 2 {
		select {
		case <-authenticator.calls:
		case <-time.After(5 * time.Second):
			t.Fatal("recovery request did not reach service")
		}
	}
	select {
	case mail := <-cfg.Sender.(testSender).messages:
		if mail.to != "known@example.com" {
			t.Fatal("delivery did not use the eligible address")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("eligible account did not receive mail")
	}
	repeated := post(handler, "/forgot-password", url.Values{"email": {"KNOWN@example.com"}}, nil)
	if repeated.Code != known.Code || repeated.Body.String() != known.Body.String() {
		t.Fatal("email quota changed public response")
	}
	select {
	case <-authenticator.calls:
		t.Fatal("case-insensitive email quota was not enforced")
	default:
	}
	for i := 3; i < 10; i++ {
		post(handler, "/forgot-password", url.Values{"email": {"x@example.com"}}, nil)
	}
	if got := post(handler, "/forgot-password", nil, nil).Code; got != http.StatusTooManyRequests {
		t.Fatalf("IP quota status = %d", got)
	}
	crossSite := post(handler, "/reset-password", nil, func(r *http.Request) {
		r.Header.Set("Sec-Fetch-Site", "cross-site")
	})
	if crossSite.Code != http.StatusForbidden {
		t.Fatalf("cross-site request = %d", crossSite.Code)
	}
	for _, path := range []string{"/login", "/forgot-password", "/reset-password"} {
		response := get(handler, path)
		if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s: %d", path, response.Code)
		}
	}
	if !strings.Contains(get(handler, "/login").Body.String(), "Forgot your password?") {
		t.Fatal("missing recovery link")
	}
	disabled := New(newFakeAuth(), Options{Actions: &fakeActionService{}, Presentation: testPresentation{}}).Handler()
	if get(disabled, "/forgot-password").Code != http.StatusNotFound ||
		strings.Contains(get(disabled, "/login").Body.String(), "Forgot your password?") {
		t.Fatal("recovery visible when disabled")
	}
	standalone := New(authenticator, Options{Recovery: cfg}).Handler()
	for _, path := range []string{"/forgot-password", "/reset-password"} {
		response := get(standalone, path)
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), `href="/login"`) ||
			strings.Contains(response.Body.String(), `href="/"`) {
			t.Fatalf("standalone recovery page %s: %d", path, response.Code)
		}
	}
	standaloneResponse := post(standalone, "/forgot-password", url.Values{"email": {"another@example.com"}, "_html": {"1"}}, nil)
	if !strings.Contains(standaloneResponse.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(standaloneResponse.Body.String(), recoveryRequestMessage) {
		t.Fatal("standalone recovery form did not render the result")
	}
}

func TestRecoveryCapacityDoesNotConsumeAddressQuota(t *testing.T) {
	stub := recoveryStub{fakeAuth: newFakeAuth(), calls: make(chan string, 1)}
	cfg := &recovery.Config{PublicOrigin: "https://game.example", Sender: testSender{make(chan resetMail, 1)}}
	server := New(stub, Options{Recovery: cfg})
	for i := 0; i < cap(server.recoverySlots); i++ {
		server.recoverySlots <- struct{}{}
	}
	handler := server.Handler()
	form := url.Values{"email": {"known@example.com"}}
	first := post(handler, "/forgot-password", form, nil)
	<-server.recoverySlots
	second := post(handler, "/forgot-password", form, nil)
	if first.Code != http.StatusOK || first.Body.String() != second.Body.String() {
		t.Fatal("capacity changed public response")
	}
	select {
	case <-stub.calls:
	case <-time.After(5 * time.Second):
		t.Fatal("capacity failure consumed address quota")
	}
	select {
	case <-cfg.Sender.(testSender).messages:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery did not complete")
	}
}

func TestRecoveryEndToEnd(t *testing.T) {
	database := testdb.Open(t)
	service, err := auth.NewService(database)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const email = "Player@example.invalid"
	if _, err := service.Register(ctx, "player", email, "original-password"); err != nil {
		t.Fatal(err)
	}
	session, err := service.Login(ctx, "player", "original-password")
	if err != nil {
		t.Fatal(err)
	}
	mails := make(chan resetMail, 1)
	cfg := &recovery.Config{PublicOrigin: "https://game.example", Sender: testSender{mails}}
	handler := New(service, Options{Recovery: cfg, Actions: &fakeActionService{}, Presentation: testPresentation{}}).Handler()
	response := post(handler, "/forgot-password", url.Values{"email": {"player@example.invalid"}, "_html": {"1"}}, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), recoveryRequestMessage) {
		t.Fatalf("request: %d", response.Code)
	}
	var mail resetMail
	select {
	case mail = <-mails:
	case <-time.After(10 * time.Second):
		t.Fatal("no recovery mail")
	}
	if mail.to != email || mail.url != "https://game.example/reset-password" || strings.Contains(mail.url, mail.code) ||
		strings.Contains(response.Body.String(), mail.code) {
		t.Fatal("unexpected recipient, reset URL, or token exposure")
	}
	form := url.Values{"code": {mail.code}, "password": {"new-password"}, "confirm_password": {"different"}}
	if got := post(handler, "/reset-password", form, nil).Code; got != http.StatusBadRequest {
		t.Fatalf("mismatched confirmation = %d", got)
	}
	form.Set("confirm_password", "new-password")
	form.Set("_html", "1")
	response = post(handler, "/reset-password", form, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Your password has been changed") ||
		strings.Contains(response.Body.String(), mail.code) {
		t.Fatalf("reset: %d", response.Code)
	}
	if _, err := service.Authenticate(ctx, session); !errors.Is(err, auth.ErrNoSession) {
		t.Fatalf("session survived reset: %v", err)
	}
	if _, err := service.Login(ctx, "player", "new-password"); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
	if got := post(handler, "/reset-password", form, nil).Code; got != http.StatusBadRequest {
		t.Fatalf("replayed reset = %d", got)
	}
}
