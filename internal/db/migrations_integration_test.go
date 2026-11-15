package db_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/db"
	"github.com/The-FKing-Projects/StillWorld/internal/testdb"
	"github.com/pressly/goose/v3"
)

// migrationAdvisoryLockID matches internal/db.migrationLockID (0x5354494c4c574f52).
const migrationAdvisoryLockID = int64(0x5354494c4c574f52)

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

func TestEmailVerificationMigrationInvalidatesLegacyResetCodes(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	provider, err := goose.NewProvider(goose.DialectPostgres, d, os.DirFS("migrations"), goose.WithTableName("goose_db_version"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `WITH a AS (
		INSERT INTO accounts (username, email, password_hash) VALUES ('legacy', 'legacy@example.invalid', 'hash') RETURNING id)
		INSERT INTO password_reset_tokens (account_id, token_hash, expires_at)
		SELECT id, decode(repeat('00', 32), 'hex'), now() + interval '1 hour' FROM a`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	var verified bool
	var tokens int
	if err := d.QueryRowContext(ctx, `SELECT email_verified_at IS NOT NULL,
		(SELECT count(*) FROM password_reset_tokens) FROM accounts WHERE username = 'legacy'`).Scan(&verified, &tokens); err != nil || verified || tokens != 0 {
		t.Fatalf("legacy verification=%v tokens=%d: %v", verified, tokens, err)
	}
	// Migration 00009 (pending signups) can roll back freely; 00008 must not
	// while accounts still have email addresses.
	if _, err := provider.DownTo(ctx, 7); err == nil {
		t.Fatal("downgrade allowed removal of verification with stored addresses")
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

func TestConcurrentMigratorWaitsOnAdvisoryLock(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	migrations := fstest.MapFS{
		"00001_counter.sql": &fstest.MapFile{Data: []byte(`-- +goose Up
CREATE TABLE game_lock_probe (id BIGINT PRIMARY KEY);
-- +goose Down
DROP TABLE game_lock_probe;
`)},
	}

	conn, err := d.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var acquired bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, migrationAdvisoryLockID).Scan(&acquired); err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("could not acquire migration advisory lock for contention test")
	}
	defer func() {
		var unlocked bool
		_ = conn.QueryRowContext(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationAdvisoryLockID).Scan(&unlocked)
	}()

	blockedCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	err = db.MigrateGameDeployment(blockedCtx, d, "counter", migrations)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked migrator error = %v, want %v", err, context.DeadlineExceeded)
	}

	var unlocked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_advisory_unlock($1)`, migrationAdvisoryLockID).Scan(&unlocked); err != nil || !unlocked {
		t.Fatalf("release held lock: unlocked=%v err=%v", unlocked, err)
	}
	if err := db.MigrateGameDeployment(ctx, d, "counter", migrations); err != nil {
		t.Fatalf("migrate after lock release: %v", err)
	}
	if err := db.CheckGameDeploymentReady(ctx, d, "counter", migrations); err != nil {
		t.Fatalf("readiness after migrate: %v", err)
	}
	if held := advisoryLockHeld(t, d); held {
		t.Fatal("migration advisory lock still held after successful migrate")
	}
}

func TestGameMigrationFailureRetainsFrameworkAndRefusesServing(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	failing := fstest.MapFS{
		"00001_fail.sql": &fstest.MapFile{Data: []byte(`-- +goose Up
SELECT 1/0;
-- +goose Down
SELECT 1;
`)},
	}
	if err := db.MigrateGameDeployment(ctx, d, "counter", failing); err == nil {
		t.Fatal("expected game migration failure")
	}

	var accountsTable string
	if err := d.QueryRowContext(ctx, `SELECT to_regclass('accounts')::text`).Scan(&accountsTable); err != nil || accountsTable == "" {
		t.Fatalf("framework tables missing after game failure: %q %v", accountsTable, err)
	}
	var gameApplied int
	if err := d.QueryRowContext(ctx,
		`SELECT count(*) FROM game_goose_db_version WHERE version_id > 0 AND is_applied`).Scan(&gameApplied); err != nil {
		t.Fatalf("read game history after failure: %v", err)
	}
	if gameApplied != 0 {
		t.Fatalf("failed game migration left %d applied versions", gameApplied)
	}
	if err := db.CheckGameDeploymentReady(ctx, d, "counter", failing); !errors.Is(err, db.ErrMigrationsNotReady) {
		t.Fatalf("readiness after failed game migrate = %v, want %v", err, db.ErrMigrationsNotReady)
	}

	fixed := fstest.MapFS{
		"00001_counter.sql": &fstest.MapFile{Data: []byte(`-- +goose Up
CREATE TABLE game_after_failure (id BIGINT PRIMARY KEY);
-- +goose Down
DROP TABLE game_after_failure;
`)},
	}
	if err := db.MigrateGameDeployment(ctx, d, "counter", fixed); err != nil {
		t.Fatalf("recover with fixed game migrations: %v", err)
	}
	if err := db.CheckGameDeploymentReady(ctx, d, "counter", fixed); err != nil {
		t.Fatalf("readiness after recovery: %v", err)
	}
}

func advisoryLockHeld(t *testing.T, d *sql.DB) bool {
	t.Helper()
	// pg_advisory_lock(bigint) appears as two int4 keys in pg_locks.
	high := int64(uint64(migrationAdvisoryLockID) >> 32)
	low := int64(uint64(migrationAdvisoryLockID) & 0xffffffff)
	var held bool
	if err := d.QueryRowContext(context.Background(),
		`SELECT EXISTS (
			SELECT 1 FROM pg_locks
			 WHERE locktype = 'advisory'
			   AND classid = $1::oid
			   AND objid = $2::oid
			   AND granted
		)`, high, low).Scan(&held); err != nil {
		t.Fatal(err)
	}
	return held
}
