// Package testdb provides isolated PostgreSQL schemas for integration tests.
package testdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"testing"

	"github.com/The-FKing-Projects/StillWorld/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func Open(t *testing.T) *sql.DB {
	t.Helper()
	url := os.Getenv("STILLWORLD_DATABASE_URL")
	if url == "" {
		t.Skip("STILLWORLD_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	schema := "test_" + hex.EncodeToString(b[:])
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), `DROP SCHEMA "`+schema+`" CASCADE`); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		if err := admin.Close(); err != nil {
			t.Errorf("close admin connection: %v", err)
		}
	})
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams["search_path"] = schema
	d := stdlib.OpenDB(*cfg)
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})
	if err := db.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	return d
}
