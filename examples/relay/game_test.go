package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestDecodeEmptyInput(t *testing.T) {
	tests := []struct {
		input string
		valid bool
	}{
		{input: `{}`, valid: true},
		{input: `{ }`, valid: true},
		{input: `null`},
		{input: `{"site_id":1}`},
		{input: `{} {}`},
		{input: ``},
	}
	for _, test := range tests {
		err := decodeEmptyInput(json.RawMessage(test.input))
		if (err == nil) != test.valid {
			t.Errorf("decodeEmptyInput(%q) error = %v, valid = %t", test.input, err, test.valid)
		}
	}
}

func TestTwoPlayersRestoreSharedRelayNetwork(t *testing.T) {
	database := openIsolatedDatabase(t)
	migrations, err := fs.Sub(embeddedMigrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	app, err := newApplication(database, migrations, bytes.Repeat([]byte{0x42}, 32), true, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := app.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	appHandler, err := app.Handler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newHTTPHandler(database, appHandler, io.Discard))
	defer server.Close()

	first := newPlayer(t, server.URL, "relay_first")
	second := newPlayer(t, server.URL, "relay_second")

	status, outcome := submitAction(t, first, server.URL, "relay.repair")
	if status != http.StatusUnprocessableEntity ||
		outcome.Rejection == nil || outcome.Rejection.Code != "relay.need_salvage" {
		t.Fatalf("repair without salvage = status %d, outcome %+v", status, outcome)
	}

	for range 4 {
		assertActionSucceeded(t, first, server.URL, "relay.salvage")
	}
	for range 2 {
		assertActionSucceeded(t, second, server.URL, "relay.salvage")
	}
	assertActionSucceeded(t, first, server.URL, "relay.repair")
	assertActionSucceeded(t, second, server.URL, "relay.repair")
	assertActionSucceeded(t, first, server.URL, "relay.repair")

	for range 2 {
		assertActionSucceeded(t, first, server.URL, "relay.salvage")
		assertActionSucceeded(t, second, server.URL, "relay.salvage")
	}
	assertActionSucceeded(t, first, server.URL, "relay.repair")
	for range 2 {
		assertActionSucceeded(t, second, server.URL, "relay.salvage")
	}
	assertActionSucceeded(t, second, server.URL, "relay.repair")
	assertActionSucceeded(t, second, server.URL, "relay.repair")

	for range 2 {
		assertActionSucceeded(t, first, server.URL, "relay.salvage")
		assertActionSucceeded(t, second, server.URL, "relay.salvage")
	}
	assertActionSucceeded(t, first, server.URL, "relay.repair")
	for range 2 {
		assertActionSucceeded(t, second, server.URL, "relay.salvage")
	}
	assertActionSucceeded(t, second, server.URL, "relay.repair")
	assertActionSucceeded(t, second, server.URL, "relay.repair")

	response, err := http.Get(server.URL + "/api/world")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("world status = %d", response.StatusCode)
	}
	var world relayWorld
	if err := json.NewDecoder(response.Body).Decode(&world); err != nil {
		t.Fatal(err)
	}
	if !world.Complete || len(world.Sites) != 3 {
		t.Fatalf("world after shared repairs = %+v", world)
	}
	for _, site := range world.Sites {
		if !site.Complete || site.Active || site.CompletedWork != site.RequiredWork {
			t.Fatalf("incomplete relay in finished network: %+v", site)
		}
	}

	var firstTurns, secondTurns int64
	if err := database.QueryRowContext(ctx,
		`SELECT turns_balance::bigint FROM accounts WHERE username = 'relay_first'`).Scan(&firstTurns); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx,
		`SELECT turns_balance::bigint FROM accounts WHERE username = 'relay_second'`).Scan(&secondTurns); err != nil {
		t.Fatal(err)
	}
	if firstTurns != 4 || secondTurns != 0 {
		t.Fatalf("remaining Turns = (%d, %d), want (4, 0)", firstTurns, secondTurns)
	}
}

type actionOutcome struct {
	Status    string            `json:"status"`
	Result    json.RawMessage   `json:"result"`
	Rejection *rejectionPayload `json:"rejection"`
}

type rejectionPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func assertActionSucceeded(t *testing.T, client *http.Client, serverURL, name string) {
	t.Helper()
	status, outcome := submitAction(t, client, serverURL, name)
	if status != http.StatusOK || outcome.Status != "success" {
		t.Fatalf("%s = status %d, outcome %+v", name, status, outcome)
	}
}

func submitAction(t *testing.T, client *http.Client, serverURL, name string) (int, actionOutcome) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, serverURL+"/api/action-requests/"+name, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Stillworld-Request", "1")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var token struct {
		RequestID string `json:"request_id"`
	}
	decodeErr := json.NewDecoder(response.Body).Decode(&token)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || token.RequestID == "" {
		t.Fatalf("issue request ID for %s: status %d, token %+v, error %v",
			name, response.StatusCode, token, decodeErr)
	}

	request, err = http.NewRequest(http.MethodPost, serverURL+"/api/actions/"+name, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Stillworld-Request", "1")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", token.RequestID)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var outcome actionOutcome
	if err := json.NewDecoder(response.Body).Decode(&outcome); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, outcome
}

func newPlayer(t *testing.T, serverURL, username string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	form := url.Values{
		"username": {username},
		"password": {"relay-test-password"},
	}
	for _, endpoint := range []struct {
		path   string
		status int
	}{
		{path: "/signup", status: http.StatusCreated},
		{path: "/login", status: http.StatusOK},
	} {
		request, err := http.NewRequest(http.MethodPost, serverURL+endpoint.path, strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("X-Stillworld-Request", "1")
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != endpoint.status {
			t.Fatalf("%s %s: status %d, want %d: %s",
				http.MethodPost, endpoint.path, response.StatusCode, endpoint.status, body)
		}
	}
	return client
}

func openIsolatedDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("STILLWORLD_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set STILLWORLD_DATABASE_URL to run the PostgreSQL integration test")
	}
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("relay_test_%d", time.Now().UnixNano())
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
