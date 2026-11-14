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

func TestEmailVerificationHTTPFlow(t *testing.T) {
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
	if _, err := service.Register(ctx, "other", "other@example.invalid", "original-password"); err != nil {
		t.Fatal(err)
	}
	mails := make(chan resetMail, 2)
	options := Options{Recovery: &recovery.Config{PublicOrigin: "https://game.example", Sender: testSender{mails}}, SharedRateLimits: database}
	first := New(service, options).Handler()
	second := New(service, options).Handler()
	if got := post(first, "/verify-email/request", nil, nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("anonymous request = %d", got)
	}
	player := sessionCookie(t, post(first, "/login", url.Values{"username": {"player"}, "password": {"original-password"}}, nil))
	other := sessionCookie(t, post(first, "/login", url.Values{"username": {"other"}, "password": {"original-password"}}, nil))
	withPlayer := func(r *http.Request) { r.AddCookie(player) }
	withOther := func(r *http.Request) { r.AddCookie(other) }
	crossSite := post(first, "/verify-email/request", nil, func(r *http.Request) {
		withPlayer(r)
		r.Header.Set("Sec-Fetch-Site", "cross-site")
	})
	if crossSite.Code != http.StatusForbidden {
		t.Fatalf("cross-site request = %d", crossSite.Code)
	}
	page := get(first, "/verify-email", player)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Player@example.invalid") || !strings.Contains(page.Body.String(), "Send verification code") {
		t.Fatalf("verification page = %d: %s", page.Code, page.Body)
	}
	if _, err := service.IssueResetToken(ctx, email); !errors.Is(err, auth.ErrRecoveryUnavailable) {
		t.Fatalf("unverified account can recover: %v", err)
	}
	response := post(first, "/verify-email/request", url.Values{"_html": {"1"}}, withPlayer)
	if response.Code != http.StatusOK {
		t.Fatalf("request = %d: %s", response.Code, response.Body)
	}
	var mail resetMail
	select {
	case mail = <-mails:
	case <-time.After(5 * time.Second):
		t.Fatal("verification email not sent")
	}
	if mail.to != email || mail.url != "https://game.example/verify-email" || strings.Contains(mail.url, mail.code) || strings.Contains(response.Body.String(), mail.code) {
		t.Fatal("verification code exposed or delivery address incorrect")
	}
	if got := post(second, "/verify-email/request", nil, withPlayer).Code; got != http.StatusTooManyRequests {
		t.Fatalf("shared verification request limit = %d", got)
	}
	form := url.Values{"code": {mail.code}, "_html": {"1"}}
	if got := post(second, "/verify-email", form, withOther).Code; got != http.StatusBadRequest {
		t.Fatalf("other account accepted code = %d", got)
	}
	response = post(second, "/verify-email", form, withPlayer)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "verified for password recovery") {
		t.Fatalf("verify = %d: %s", response.Code, response.Body)
	}
	if got := post(first, "/verify-email", form, withPlayer).Code; got != http.StatusBadRequest {
		t.Fatalf("replayed code = %d", got)
	}
	if _, err := service.IssueResetToken(ctx, email); err != nil {
		t.Fatalf("verified account cannot recover: %v", err)
	}
	if _, err := database.ExecContext(ctx, `DROP TABLE stillworld_rate_limits`); err != nil {
		t.Fatal(err)
	}
	if got := post(first, "/verify-email/request", nil, withOther).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("verification did not fail closed: %d", got)
	}
}
