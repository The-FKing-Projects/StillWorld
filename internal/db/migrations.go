package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/actionname"
	"github.com/pressly/goose/v3"
)

const (
	frameworkHistory = "goose_db_version"
	gameHistory      = "game_goose_db_version"
	migrationLockID  = int64(0x5354494c4c574f52)
)

var (
	ErrMigrationPoolTooSmall = errors.New("db: migration pool must allow at least two open connections")
	ErrGameMigrationRequired = errors.New("db: this deployment is bound to a game; use its migration coordinator")
	ErrGameMismatch          = errors.New("db: database is bound to a different game")
	ErrMigrationsNotReady    = errors.New("db: migration history is not ready")
)

// MigrateGameDeployment applies framework and game migrations under one lock,
// binds an uninitialized schema to gameID, and verifies both histories.
func MigrateGameDeployment(ctx context.Context, d *sql.DB, gameID string, gameMigrations fs.FS) error {
	if !actionname.ValidGameID(gameID) {
		return fmt.Errorf("db: invalid game ID %q", gameID)
	}
	if d == nil {
		return errors.New("db: database is required")
	}
	gameProvider, err := newProvider(d, gameMigrations, gameHistory)
	if err != nil {
		return err
	}
	return withMigrationLock(ctx, d, func() error {
		if err := requireGameID(ctx, d, gameID); err != nil {
			return err
		}
		if err := migrateFramework(ctx, d); err != nil {
			return err
		}
		if err := bindGameID(ctx, d, gameID); err != nil {
			return err
		}
		framework, err := frameworkProvider(d)
		if err != nil {
			return err
		}
		if err := verifyHistory(ctx, d, framework, frameworkHistory); err != nil {
			return fmt.Errorf("verify framework migrations before game migrations: %w", err)
		}
		if _, err := gameProvider.Up(ctx); err != nil {
			return fmt.Errorf("apply game migrations: %w", err)
		}
		if err := verifyHistory(ctx, d, framework, frameworkHistory); err != nil {
			return fmt.Errorf("verify framework migrations: %w", err)
		}
		if err := verifyHistory(ctx, d, gameProvider, gameHistory); err != nil {
			return fmt.Errorf("verify game migrations: %w", err)
		}
		return nil
	})
}

// CheckGameDeploymentReady verifies the binding and both migration histories
// while holding the same advisory lock used by migration entry points.
func CheckGameDeploymentReady(ctx context.Context, d *sql.DB, gameID string, gameMigrations fs.FS) error {
	if !actionname.ValidGameID(gameID) {
		return fmt.Errorf("db: invalid game ID %q", gameID)
	}
	if d == nil {
		return errors.New("db: database is required")
	}
	gameProvider, err := newProvider(d, gameMigrations, gameHistory)
	if err != nil {
		return err
	}
	return withMigrationLock(ctx, d, func() error {
		if err := requireGameID(ctx, d, gameID); err != nil {
			return err
		}
		framework, err := frameworkProvider(d)
		if err != nil {
			return err
		}
		if err := verifyHistory(ctx, d, framework, frameworkHistory); err != nil {
			return fmt.Errorf("verify framework readiness: %w", err)
		}
		if err := verifyHistory(ctx, d, gameProvider, gameHistory); err != nil {
			return fmt.Errorf("verify game readiness: %w", err)
		}
		return nil
	})
}

func migrateFramework(ctx context.Context, d *sql.DB) error {
	provider, err := frameworkProvider(d)
	if err != nil {
		return err
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply framework migrations: %w", err)
	}
	if err := verifyHistory(ctx, d, provider, frameworkHistory); err != nil {
		return fmt.Errorf("verify framework migrations: %w", err)
	}
	return nil
}

func frameworkProvider(d *sql.DB) (*goose.Provider, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("locate framework migrations: %w", err)
	}
	return newProvider(d, sub, frameworkHistory)
}

func newProvider(d *sql.DB, migrations fs.FS, table string) (*goose.Provider, error) {
	if migrations == nil {
		return nil, errors.New("db: migration filesystem is required")
	}
	if err := validateMigrationFilesystem(migrations); err != nil {
		return nil, err
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		d,
		migrations,
		goose.WithTableName(table),
	)
	if err != nil {
		return nil, fmt.Errorf("create %s provider: %w", table, err)
	}
	sources := provider.ListSources()
	seen := make(map[int64]struct{}, len(sources))
	for _, source := range sources {
		if source.Version <= 0 {
			return nil, fmt.Errorf("db: migration version must be positive: %q", source.Path)
		}
		if _, ok := seen[source.Version]; ok {
			return nil, fmt.Errorf("db: duplicate migration version %d", source.Version)
		}
		seen[source.Version] = struct{}{}
	}
	return provider, nil
}

func validateMigrationFilesystem(migrations fs.FS) error {
	return fs.WalkDir(migrations, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".sql") {
			return nil
		}
		content, err := fs.ReadFile(migrations, path)
		if err != nil {
			return fmt.Errorf("read migration %q: %w", path, err)
		}
		for _, line := range strings.Split(string(content), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "--") &&
				strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(line, "--")), "+goose NO TRANSACTION") {
				return fmt.Errorf("db: nontransactional migration %q is not supported", path)
			}
		}
		return nil
	})
}

func verifyHistory(ctx context.Context, d *sql.DB, provider *goose.Provider, table string) error {
	statuses, err := provider.Status(ctx)
	if err != nil {
		return fmt.Errorf("read %s status: %w", table, err)
	}
	expected := make(map[int64]struct{}, len(statuses))
	for _, status := range statuses {
		if status.Source == nil || status.Source.Version <= 0 {
			return fmt.Errorf("%w: invalid source in %s", ErrMigrationsNotReady, table)
		}
		if status.State != goose.StateApplied {
			return fmt.Errorf("%w: %s migration %d is %s", ErrMigrationsNotReady, table, status.Source.Version, status.State)
		}
		expected[status.Source.Version] = struct{}{}
	}
	current, target, err := provider.GetVersions(ctx)
	if err != nil {
		return fmt.Errorf("read %s versions: %w", table, err)
	}
	if current != target {
		return fmt.Errorf("%w: %s is at %d, executable targets %d", ErrMigrationsNotReady, table, current, target)
	}
	rows, err := d.QueryContext(ctx,
		`SELECT version_id, is_applied FROM `+table+` ORDER BY id DESC`)
	if err != nil {
		return fmt.Errorf("read applied versions from %s: %w", table, err)
	}
	defer rows.Close()
	actual := make(map[int64]struct{}, len(expected))
	seen := make(map[int64]struct{}, len(expected))
	for rows.Next() {
		var version int64
		var applied bool
		if err := rows.Scan(&version, &applied); err != nil {
			return fmt.Errorf("scan applied version from %s: %w", table, err)
		}
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}
		if applied && version > 0 {
			actual[version] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate applied versions from %s: %w", table, err)
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("%w: %s has %d applied versions, executable contains %d", ErrMigrationsNotReady, table, len(actual), len(expected))
	}
	for version := range actual {
		if _, ok := expected[version]; !ok {
			return fmt.Errorf("%w: %s contains unknown applied version %d", ErrMigrationsNotReady, table, version)
		}
	}
	return nil
}

func requireGameID(ctx context.Context, d *sql.DB, expected string) error {
	gameID, bound, err := deploymentGameID(ctx, d)
	if err != nil {
		return err
	}
	if bound && gameID != expected {
		return fmt.Errorf("%w: database has %q, executable is %q", ErrGameMismatch, gameID, expected)
	}
	return nil
}

func deploymentGameID(ctx context.Context, d *sql.DB) (string, bool, error) {
	var exists bool
	if err := d.QueryRowContext(ctx,
		`SELECT to_regclass('stillworld_deployment') IS NOT NULL`).Scan(&exists); err != nil {
		return "", false, fmt.Errorf("check deployment metadata table: %w", err)
	}
	if !exists {
		return "", false, nil
	}
	var gameID string
	err := d.QueryRowContext(ctx,
		`SELECT game_id FROM stillworld_deployment WHERE id = 1`).Scan(&gameID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read deployment game ID: %w", err)
	}
	return gameID, true, nil
}

func bindGameID(ctx context.Context, d *sql.DB, gameID string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin deployment binding: %w", err)
	}
	var current string
	err = tx.QueryRowContext(ctx,
		`SELECT game_id FROM stillworld_deployment WHERE id = 1 FOR UPDATE`).Scan(&current)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO stillworld_deployment (id, game_id) VALUES (1, $1)`, gameID); err != nil {
			return rollback(tx, fmt.Errorf("bind deployment game ID: %w", err))
		}
	case err != nil:
		return rollback(tx, fmt.Errorf("read deployment game ID for binding: %w", err))
	case current != gameID:
		return rollback(tx, fmt.Errorf("%w: database has %q, executable is %q", ErrGameMismatch, current, gameID))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit deployment binding: %w", err)
	}
	return nil
}

func rollback(tx *sql.Tx, cause error) error {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return errors.Join(cause, fmt.Errorf("roll back deployment binding: %w", err))
	}
	return cause
}

func withMigrationLock(ctx context.Context, d *sql.DB, run func() error) error {
	if d == nil {
		return errors.New("db: database is required")
	}
	if d.Stats().MaxOpenConnections == 1 {
		return ErrMigrationPoolTooSmall
	}
	conn, err := d.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	acquired, err := acquireMigrationLock(ctx, conn)
	if err != nil {
		discardConnection(conn)
		return err
	}
	if !acquired {
		_ = conn.Close()
		return ctx.Err()
	}

	return func() (runErr error) {
		defer func() {
			runErr = errors.Join(runErr, releaseMigrationLock(conn))
		}()
		return run()
	}()
}

func releaseMigrationLock(conn *sql.Conn) error {
	releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var unlocked bool
	unlockErr := conn.QueryRowContext(releaseCtx,
		`SELECT pg_advisory_unlock($1)`, migrationLockID).Scan(&unlocked)
	if unlockErr != nil || !unlocked {
		discardConnection(conn)
		if unlockErr != nil {
			unlockErr = fmt.Errorf("release migration advisory lock: %w", unlockErr)
		} else {
			unlockErr = errors.New("release migration advisory lock: lock was not held")
		}
		return unlockErr
	}
	if err := conn.Close(); err != nil {
		return fmt.Errorf("close migration connection: %w", err)
	}
	return nil
}

func acquireMigrationLock(ctx context.Context, conn *sql.Conn) (bool, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		var acquired bool
		err := conn.QueryRowContext(ctx,
			`SELECT pg_try_advisory_lock($1)`, migrationLockID).Scan(&acquired)
		if err != nil {
			return false, fmt.Errorf("acquire migration advisory lock: %w", err)
		}
		if acquired {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
	}
}

func discardConnection(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}
