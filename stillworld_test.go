package stillworld

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/testdb"
)

func testOptions() Options {
	return Options{
		Turns:                 TurnsPolicy{Cap: 10, Interval: time.Hour},
		RequestTokenActiveKey: "active",
		RequestTokenKeys:      map[string][]byte{"active": make([]byte, 32)},
	}
}

func TestNewRequiresDatabase(t *testing.T) {
	_, err := New(nil, Game{ID: "counter", Migrations: fstest.MapFS{}}, testOptions())
	if err == nil {
		t.Fatal("New accepted a nil database")
	}
}

func TestNewRejectsInvalidGame(t *testing.T) {
	d, err := sql.Open("pgx", "postgres://stillworld:stillworld@127.0.0.1:1/stillworld")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	_, err = New(d, Game{ID: "Invalid-ID", Migrations: fstest.MapFS{}}, testOptions())
	if err == nil {
		t.Fatalf("New error = %v, want invalid game configuration", err)
	}
}

func TestNewBuildsApplicationWithoutConnectingOrMigrating(t *testing.T) {
	d, err := sql.Open("pgx", "postgres://stillworld:stillworld@127.0.0.1:1/stillworld")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	app, err := New(d, Game{ID: "counter", Migrations: fstest.MapFS{}}, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if app == nil || app.gameID != "counter" {
		t.Fatalf("application = %+v", app)
	}
	if stats := d.Stats(); stats.OpenConnections != 0 {
		t.Fatalf("New opened %d database connections", stats.OpenConnections)
	}
}

func TestAppMigrateAndHandlerReadiness(t *testing.T) {
	d := testdb.Open(t)
	game := Game{
		ID: "counter",
		Migrations: fstest.MapFS{
			"00001_counter.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE game_counters (account_id BIGINT PRIMARY KEY REFERENCES accounts (id));\n-- +goose Down\nDROP TABLE game_counters;\n")},
		},
	}
	app, err := New(d, game, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Handler(context.Background()); err == nil {
		t.Fatal("Handler accepted a schema without game migrations")
	}
	if err := app.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	accountID, err := app.auth.Register(context.Background(), "starter", "", "password123")
	if err != nil {
		t.Fatal(err)
	}
	var initialTurns int64
	if err := d.QueryRowContext(context.Background(),
		`SELECT turns_balance::bigint FROM accounts WHERE id = $1`, accountID).Scan(&initialTurns); err != nil {
		t.Fatal(err)
	}
	if initialTurns != testOptions().Turns.Cap {
		t.Fatalf("new account starts with %d Turns, want cap %d", initialTurns, testOptions().Turns.Cap)
	}
	handler, err := app.Handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("health response = %d: %s", response.Code, response.Body)
	}
}
