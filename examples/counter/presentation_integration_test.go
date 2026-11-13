package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/The-FKing-Projects/StillWorld"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestCounterHTMLActionWithIntegerInput(t *testing.T) {
	database := openCounterTestDatabase(t)
	migrations, err := fs.Sub(embeddedMigrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	app, err := stillworld.New(database, stillworld.Game{
		ID: "counter", Migrations: migrations,
		Actions: []stillworld.Registration{{Name: "counter.increment", Handler: incrementAction{}}},
	}, stillworld.Options{
		Turns:                 stillworld.TurnsPolicy{Cap: 100, Interval: time.Hour},
		RequestTokenActiveKey: "v1", RequestTokenKeys: map[string][]byte{"v1": bytes.Repeat([]byte{0x42}, 32)},
		InsecureCookies: true, Presentation: counterPresentation{database: database},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := app.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	handler, err := app.Handler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	credentials := url.Values{"username": {"counter_player"}, "password": {"counter-test-password"}, "_html": {"1"}}
	for _, path := range []string{"/signup", "/login"} {
		response, body := counterRequest(t, client, http.MethodPost, server.URL+path, credentials.Encode(), server.URL)
		if response.StatusCode != http.StatusSeeOther {
			t.Fatalf("%s = %d: %s", path, response.StatusCode, body)
		}
	}

	response, body := counterRequest(t, client, http.MethodGet, server.URL+"/", "", "")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "Current value: 0") ||
		!strings.Contains(body, `name="amount" type="number" value="1" min="1" max="100"`) {
		t.Fatalf("counter page = %d: %s", response.StatusCode, body)
	}
	tokenPattern := regexp.MustCompile(`name="request_id" value="([^"]+)"`)
	match := tokenPattern.FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("missing request ID in %s", body)
	}
	form := url.Values{"request_id": {match[1]}, "amount": {"3"}}
	response, body = counterRequest(t, client, http.MethodPost, server.URL+"/actions/counter.increment", form.Encode(), server.URL)
	resultURL := response.Header.Get("Location")
	if response.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resultURL, "/actions/results/") {
		t.Fatalf("submit = %d %q: %s", response.StatusCode, resultURL, body)
	}
	response, body = counterRequest(t, client, http.MethodGet, server.URL+resultURL, "", "")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "Your counter is now 3.") {
		t.Fatalf("result = %d: %s", response.StatusCode, body)
	}
	response, body = counterRequest(t, client, http.MethodPost, server.URL+"/actions/counter.increment", form.Encode(), server.URL)
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != resultURL {
		t.Fatalf("replay = %d %q: %s", response.StatusCode, response.Header.Get("Location"), body)
	}
	form.Set("amount", "4")
	response, body = counterRequest(t, client, http.MethodPost, server.URL+"/actions/counter.increment", form.Encode(), server.URL)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("changed input = %d: %s", response.StatusCode, body)
	}
	var value, turns int64
	if err := database.QueryRow(`SELECT c.value, a.turns_balance::bigint FROM game_counters c
		JOIN accounts a ON a.id = c.account_id WHERE a.username = 'counter_player'`).Scan(&value, &turns); err != nil {
		t.Fatal(err)
	}
	if value != 3 || turns != 97 {
		t.Fatalf("counter = %d, Turns = %d; want 3 and 97", value, turns)
	}
}

func counterRequest(t *testing.T, client *http.Client, method, target, form, origin string) (*http.Response, string) {
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

func openCounterTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("STILLWORLD_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set STILLWORLD_DATABASE_URL to run the PostgreSQL integration test")
	}
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("counter_html_test_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(context.Background(), "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		if err := admin.Close(); err != nil {
			t.Errorf("close admin connection: %v", err)
		}
	})
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema
	database := stdlib.OpenDB(*config)
	database.SetMaxOpenConns(10)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close isolated database: %v", err)
		}
	})
	return database
}
