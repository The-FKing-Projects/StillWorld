package db_test

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/The-FKing-Projects/StillWorld/internal/db"
	"github.com/The-FKing-Projects/StillWorld/internal/testdb"
)

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
