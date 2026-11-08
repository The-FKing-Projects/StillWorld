package db

import (
	"context"
	"os"
	"testing"
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
