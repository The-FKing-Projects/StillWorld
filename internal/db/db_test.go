package db

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMigrate(t *testing.T) {
	url := os.Getenv("STILLWORLD_DATABASE_URL")
	if url == "" {
		t.Skip("STILLWORLD_DATABASE_URL not set")
	}

	ctx := context.Background()
	d, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// Running twice verifies migrations are idempotent.
	for i := 0; i < 2; i++ {
		if err := Migrate(ctx, d); err != nil {
			t.Fatalf("migrate run %d: %v", i+1, err)
		}
	}
}

func TestSchemaConstraints(t *testing.T) {
	url := os.Getenv("STILLWORLD_DATABASE_URL")
	if url == "" {
		t.Skip("STILLWORLD_DATABASE_URL not set")
	}

	ctx := context.Background()
	d, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}

	// Each test run uses unique names so it can share a database with other runs.
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	name := "Alice" + suffix
	var id int64
	err = d.QueryRowContext(ctx,
		`INSERT INTO accounts (username, password_hash) VALUES ($1, 'x') RETURNING id`, name).Scan(&id)
	if err != nil {
		t.Fatalf("insert account: %v", err)
	}
	defer d.ExecContext(ctx, `DELETE FROM accounts WHERE id = $1`, id)

	if _, err := d.ExecContext(ctx,
		`INSERT INTO accounts (username, password_hash) VALUES ($1, 'x')`, strings.ToLower(name)); err == nil {
		t.Error("usernames that differ only by case should conflict")
	}

	if _, err := d.ExecContext(ctx,
		`UPDATE accounts SET turns_balance = -1 WHERE id = $1`, id); err == nil {
		t.Error("negative Turns balance should be rejected")
	}

	email := "a" + suffix + "@example.com"
	if _, err := d.ExecContext(ctx, `UPDATE accounts SET email = $1 WHERE id = $2`, email, id); err != nil {
		t.Fatalf("set email: %v", err)
	}
	other := "Bob" + suffix
	if _, err := d.ExecContext(ctx,
		`INSERT INTO accounts (username, email, password_hash) VALUES ($1, $2, 'x')`,
		other, strings.ToUpper(email)); err == nil {
		t.Error("emails that differ only by case should conflict")
		d.ExecContext(ctx, `DELETE FROM accounts WHERE username = $1`, other)
	}

	// Accounts without an email must not conflict with each other.
	var id2 int64
	if err := d.QueryRowContext(ctx,
		`INSERT INTO accounts (username, password_hash) VALUES ($1, 'x') RETURNING id`, other).Scan(&id2); err != nil {
		t.Fatalf("second account without email: %v", err)
	}
	defer d.ExecContext(ctx, `DELETE FROM accounts WHERE id = $1`, id2)

	if _, err := d.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, account_id, expires_at) VALUES ('\x01', $1, now() + interval '1 day')`, id); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if _, err := d.ExecContext(ctx, `DELETE FROM accounts WHERE id = $1`, id); err != nil {
		t.Fatalf("delete account: %v", err)
	}
	var n int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE account_id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("sessions should cascade-delete with their account, %d remain", n)
	}
}
