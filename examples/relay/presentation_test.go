package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestHTMLGameFlow(t *testing.T) {
	database := openIsolatedDatabase(t)
	migrations, err := fs.Sub(embeddedMigrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	app, err := newApplication(database, migrations, bytes.Repeat([]byte{0x42}, 32), true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	appHandler, err := app.Handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newHTTPHandler(database, appHandler, io.Discard))
	defer server.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, body := htmlRequest(t, client, http.MethodGet, server.URL+"/", "", "")
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/login" {
		t.Fatalf("anonymous home = %d %q", response.StatusCode, response.Header.Get("Location"))
	}
	response, body = htmlRequest(t, client, http.MethodGet, server.URL+"/signup", "", "")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, `action="/signup"`) {
		t.Fatalf("signup page = %d %s", response.StatusCode, body)
	}
	form := url.Values{"_html": {"1"}, "username": {"html_player"}, "password": {"relay-test-password"}}
	response, body = htmlRequest(t, client, http.MethodPost, server.URL+"/signup", form.Encode(), server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("signup = %d %s", response.StatusCode, body)
	}
	response, body = htmlRequest(t, client, http.MethodPost, server.URL+"/login", form.Encode(), server.URL)
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/" {
		t.Fatalf("login = %d %s", response.StatusCode, body)
	}
	response, body = htmlRequest(t, client, http.MethodGet, server.URL+"/", "", "")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "North Shoal") ||
		!strings.Contains(body, "html_player") || !strings.Contains(body, "Available Turns:</strong> 20") {
		t.Fatalf("game home = %d %s", response.StatusCode, body)
	}
	expression := regexp.MustCompile(`action="/actions/relay\.salvage">\s*<input type="hidden" name="request_id" value="([^"]+)"`)
	match := expression.FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("missing salvage form token: %s", body)
	}
	actionForm := url.Values{"request_id": {match[1]}}

	response, body = htmlRequest(t, client, http.MethodPost, server.URL+"/actions/relay.salvage", actionForm.Encode(), "https://evil.example")
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin action = %d %s", response.StatusCode, body)
	}
	bad := url.Values{"request_id": {match[1], match[1]}}
	response, body = htmlRequest(t, client, http.MethodPost, server.URL+"/actions/relay.salvage", bad.Encode(), server.URL)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate request ID = %d %s", response.StatusCode, body)
	}
	response, body = htmlRequest(t, client, http.MethodPost, server.URL+"/actions/relay.salvage", actionForm.Encode(), server.URL)
	resultURL := response.Header.Get("Location")
	if response.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resultURL, "/actions/results/") || strings.Contains(resultURL, match[1]) {
		t.Fatalf("submit = %d %q %s", response.StatusCode, resultURL, body)
	}
	response, body = htmlRequest(t, client, http.MethodGet, server.URL+resultURL, "", "")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "pack now has 1 of 5") {
		t.Fatalf("result = %d %s", response.StatusCode, body)
	}
	repairExpression := regexp.MustCompile(`action="/actions/relay\.repair">\s*<input type="hidden" name="request_id" value="([^"]+)"`)
	repair := repairExpression.FindStringSubmatch(body)
	if len(repair) == 2 {
		t.Fatal("result page unexpectedly issued a new action token")
	}
	response, body = htmlRequest(t, client, http.MethodPost, server.URL+"/actions/relay.salvage", actionForm.Encode(), server.URL)
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != resultURL {
		t.Fatalf("replay = %d %q %s", response.StatusCode, response.Header.Get("Location"), body)
	}
	var salvage, turns int64
	if err := database.QueryRow(`SELECT p.salvage, a.turns_balance::bigint FROM game_relay_players p
		JOIN accounts a ON a.id = p.account_id WHERE a.username = 'html_player'`).Scan(&salvage, &turns); err != nil {
		t.Fatal(err)
	}
	if salvage != 1 || turns != 19 {
		t.Fatalf("replay changed game state: salvage=%d turns=%d", salvage, turns)
	}
	response, body = htmlRequest(t, client, http.MethodGet, server.URL+"/", "", "")
	repair = repairExpression.FindStringSubmatch(body)
	if response.StatusCode != http.StatusOK || len(repair) != 2 {
		t.Fatalf("missing repair form: %d %s", response.StatusCode, body)
	}
	response, body = htmlRequest(t, client, http.MethodPost, server.URL+"/actions/relay.repair",
		url.Values{"request_id": {repair[1]}}.Encode(), server.URL)
	rejectionURL := response.Header.Get("Location")
	if response.StatusCode != http.StatusSeeOther || !strings.HasPrefix(rejectionURL, "/actions/results/") {
		t.Fatalf("rejected action redirect = %d %q %s", response.StatusCode, rejectionURL, body)
	}
	response, body = htmlRequest(t, client, http.MethodGet, server.URL+rejectionURL, "", "")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "Collect two spare components") {
		t.Fatalf("saved rejection = %d %s", response.StatusCode, body)
	}
	other := newPlayer(t, server.URL, "other_player")
	other.CheckRedirect = client.CheckRedirect
	response, body = htmlRequest(t, other, http.MethodGet, server.URL+resultURL, "", "")
	if response.StatusCode != http.StatusNotFound || !strings.Contains(body, "does not prove") {
		t.Fatalf("other player's result = %d %s", response.StatusCode, body)
	}
	response, body = htmlRequest(t, client, http.MethodPost, server.URL+"/logout", url.Values{"_html": {"1"}}.Encode(), server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout = %d %s", response.StatusCode, body)
	}
}

func htmlRequest(t *testing.T, client *http.Client, method, target, form, origin string) (*http.Response, string) {
	t.Helper()
	request, err := http.NewRequest(method, target, strings.NewReader(form))
	if err != nil {
		t.Fatal(err)
	}
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Origin", origin)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return response, string(body)
}

func TestRelayFormAdapterRejectsUnexpectedFields(t *testing.T) {
	game := relayPresentation{}
	for _, test := range []struct {
		name   string
		fields url.Values
	}{
		{"relay.salvage", url.Values{"amount": {"1"}}},
		{"relay.repair", url.Values{"request_id": {"other"}}},
		{"relay.unknown", url.Values{}},
	} {
		if _, err := game.DecodeForm(test.name, test.fields); err == nil {
			t.Error(fmt.Sprintf("accepted unexpected form %q: %v", test.name, test.fields))
		}
	}
}
