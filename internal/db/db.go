// Package db opens the PostgreSQL database and applies schema migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open connects to PostgreSQL and verifies the connection.
func Open(ctx context.Context, url string) (*sql.DB, error) {
	d, err := sql.Open("pgx", url)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := d.PingContext(ctx); err != nil {
		d.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return d, nil
}

// Migrate applies all pending migrations.
func Migrate(ctx context.Context, d *sql.DB) error {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("locate migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, d, sub)
	if err != nil {
		return fmt.Errorf("create migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
