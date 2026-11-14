package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/The-FKing-Projects/StillWorld/internal/auth"
	"github.com/The-FKing-Projects/StillWorld/internal/testdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestRevokeSessionsCommandRequiresOneUsername(t *testing.T) {
	for _, args := range [][]string{{"revoke-sessions"}, {"revoke-sessions", "one", "two"}} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "revoke-sessions") {
			t.Errorf("args=%v code=%d error=%q", args, code, errOut.String())
		}
	}
}

func TestRevokeSessionsCommand(t *testing.T) {
	database := testdb.Open(t)
	ctx := context.Background()
	var schema string
	if err := database.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	config, err := pgx.ParseConfig(os.Getenv("STILLWORLD_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema
	connString := stdlib.RegisterConnConfig(config)
	t.Cleanup(func() { stdlib.UnregisterConnConfig(connString) })
	t.Setenv("STILLWORLD_DATABASE_URL", connString)
	service, err := auth.NewService(database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Register(ctx, "player", "", "password123"); err != nil {
		t.Fatal(err)
	}
	token, err := service.Login(ctx, "player", "password123")
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"revoke-sessions", "PLAYER"}, &out, &errOut); code != 0 ||
		!strings.Contains(out.String(), "Existing sessions revoked") {
		t.Fatalf("exit=%d output=%q error=%q", code, out.String(), errOut.String())
	}
	if _, err := service.Authenticate(ctx, token); !errors.Is(err, auth.ErrNoSession) {
		t.Fatalf("session survived command: %v", err)
	}
	token, err = service.Login(ctx, "player", "password123")
	if err != nil {
		t.Fatal(err)
	}
	errOut.Reset()
	if code := run([]string{"revoke-sessions", "player"}, failingWriter{}, &errOut); code != 1 ||
		!strings.Contains(errOut.String(), "sessions were revoked") {
		t.Fatalf("confirmation failure exit=%d error=%q", code, errOut.String())
	}
	if _, err := service.Authenticate(ctx, token); !errors.Is(err, auth.ErrNoSession) {
		t.Fatalf("output failure undid revocation: %v", err)
	}
	errOut.Reset()
	if code := run([]string{"revoke-sessions", "missing"}, &out, &errOut); code != 1 ||
		!strings.Contains(errOut.String(), auth.ErrAccountNotFound.Error()) {
		t.Fatalf("missing account exit=%d error=%q", code, errOut.String())
	}
}
