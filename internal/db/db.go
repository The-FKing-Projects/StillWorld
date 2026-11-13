// Package db opens the PostgreSQL database and applies schema migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
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
	return withMigrationLock(ctx, d, func() error {
		if _, bound, err := deploymentGameID(ctx, d); err != nil {
			return err
		} else if bound {
			return ErrGameMigrationRequired
		}
		return migrateFramework(ctx, d)
	})
}
