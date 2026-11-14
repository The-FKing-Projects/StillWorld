package db_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"testing/fstest"

	"github.com/The-FKing-Projects/StillWorld/internal/db"
	"github.com/The-FKing-Projects/StillWorld/internal/testdb"
	"github.com/pressly/goose/v3"
)

func TestAccountDisablingMigrationUpgradeAndGuardedDowngrade(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	provider, err := goose.NewProvider(goose.DialectPostgres, d, os.DirFS("migrations"), goose.WithTableName("goose_db_version"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO accounts (username, password_hash) VALUES ('migration_player', 'hash')`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 4); err != nil {
		t.Fatalf("downgrade with active accounts: %v", err)
	}
	if err := db.Migrate(ctx, d); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	var active bool
	if err := d.QueryRowContext(ctx, `SELECT disabled_at IS NULL FROM accounts WHERE username = 'migration_player'`).Scan(&active); err != nil || !active {
		t.Fatalf("existing account after upgrade: active=%v error=%v", active, err)
	}
	if _, err := d.ExecContext(ctx, `UPDATE accounts SET disabled_at = now() WHERE username = 'migration_player'`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 4); err == nil {
		t.Fatal("downgrade succeeded while an account was disabled")
	}
	if err := d.QueryRowContext(ctx, `SELECT disabled_at IS NOT NULL FROM accounts WHERE username = 'migration_player'`).Scan(&active); err != nil || !active {
		t.Fatalf("disabled account changed after rejected downgrade: disabled=%v error=%v", active, err)
	}
}

func TestGameMigrationCoordinationAndBinding(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	migrations := fstest.MapFS{
		"00001_counter.sql": &fstest.MapFile{Data: []byte(`-- +goose Up
CREATE TABLE game_counters (
    account_id BIGINT PRIMARY KEY REFERENCES accounts (id) ON DELETE CASCADE,
    value BIGINT NOT NULL DEFAULT 0
);
-- +goose Down
DROP TABLE game_counters;
`)},
	}

	if err := db.MigrateGameDeployment(ctx, d, "counter", migrations); err != nil {
		t.Fatal(err)
	}
	if err := db.CheckGameDeploymentReady(ctx, d, "counter", migrations); err != nil {
		t.Fatalf("check ready: %v", err)
	}
	if err := db.MigrateGameDeployment(ctx, d, "counter", migrations); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	if err := db.MigrateGameDeployment(ctx, d, "another_game", migrations); !errors.Is(err, db.ErrGameMismatch) {
		t.Fatalf("different game ID error = %v", err)
	}
	if err := db.Migrate(ctx, d); !errors.Is(err, db.ErrGameMigrationRequired) {
		t.Fatalf("framework-only migration error = %v", err)
	}
}

func TestReadinessRejectsDifferentMigrationHistory(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	original := fstest.MapFS{
		"00001_counter.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE game_counter_value (value BIGINT);\n-- +goose Down\nDROP TABLE game_counter_value;\n")},
	}
	if err := db.MigrateGameDeployment(ctx, d, "counter", original); err != nil {
		t.Fatal(err)
	}

	replaced := fstest.MapFS{
		"00002_replacement.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n")},
	}
	if err := db.CheckGameDeploymentReady(ctx, d, "counter", replaced); !errors.Is(err, db.ErrMigrationsNotReady) {
		t.Fatalf("readiness error = %v, want %v", err, db.ErrMigrationsNotReady)
	}
}
