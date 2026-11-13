package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"testing/fstest"
)

func TestMigrationPoolRequiresLockAndRunnerConnections(t *testing.T) {
	d, err := sql.Open("pgx", "postgres://stillworld:stillworld@127.0.0.1:1/stillworld")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SetMaxOpenConns(1)
	if err := Migrate(context.Background(), d); !errors.Is(err, ErrMigrationPoolTooSmall) {
		t.Fatalf("Migrate error = %v, want %v", err, ErrMigrationPoolTooSmall)
	}
}

func TestValidateMigrationFilesystemRejectsNontransactionalDirective(t *testing.T) {
	migrations := fstest.MapFS{
		"00001_initial.sql": &fstest.MapFile{
			Data: []byte("-- +goose Up\n-- +goose NO TRANSACTION\nSELECT 1;\n"),
		},
	}
	if err := validateMigrationFilesystem(migrations); err == nil {
		t.Fatal("expected nontransactional migration to be rejected")
	}
}

func TestProviderUsesDistinctHistoryAndPositiveUniqueVersions(t *testing.T) {
	d, err := sql.Open("pgx", "postgres://stillworld:stillworld@127.0.0.1:1/stillworld")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	provider, err := newProvider(d, fstest.MapFS{
		"00001_initial.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n")},
	}, gameHistory)
	if err != nil {
		t.Fatal(err)
	}
	if sources := provider.ListSources(); len(sources) != 1 || sources[0].Version != 1 {
		t.Fatalf("sources = %+v", sources)
	}

	if _, err := newProvider(d, fstest.MapFS{
		"00001_first.sql":  &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n")},
		"00001_second.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n")},
	}, gameHistory); err == nil {
		t.Fatal("expected duplicate migration version to be rejected")
	}
}
