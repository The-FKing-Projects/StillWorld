package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/config"
	"github.com/The-FKing-Projects/StillWorld/internal/db"
)

const maxAccountsPerPage = 1000

func listAccounts(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("list-accounts", flag.ContinueOnError)
	flags.SetOutput(stderr)
	after := flags.Int64("after", 0, "list accounts after this account ID")
	limit := flags.Int("limit", 100, "maximum accounts per page (1-1000)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *after < 0 || *limit < 1 || *limit > maxAccountsPerPage {
		fmt.Fprintln(stderr, "stillworld: invalid account listing arguments")
		return 2
	}
	cfg, err := config.FromEnv()
	if err != nil {
		fmt.Fprintln(stderr, "stillworld:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	database, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(stderr, "stillworld:", err)
		return 1
	}
	defer database.Close()
	next, err := writeAccounts(ctx, database, *after, *limit, stdout)
	if err != nil {
		fmt.Fprintln(stderr, "stillworld: list accounts:", err)
		return 1
	}
	if next != 0 {
		if _, err := fmt.Fprintf(stderr, "More accounts are available; continue with -after=%d.\n", next); err != nil {
			return 1
		}
	}
	return 0
}

func writeAccounts(ctx context.Context, database *sql.DB, after int64, limit int, output io.Writer) (int64, error) {
	if after < 0 || limit < 1 || limit > maxAccountsPerPage {
		return 0, errors.New("invalid account listing arguments")
	}
	rows, err := database.QueryContext(ctx,
		`SELECT id, username FROM accounts WHERE id > $1 ORDER BY id LIMIT $2`, after, limit+1)
	if err != nil {
		return 0, fmt.Errorf("query accounts: %w", err)
	}
	defer rows.Close()
	if _, err := fmt.Fprintln(output, "ID\tUSERNAME"); err != nil {
		return 0, fmt.Errorf("write account listing: %w", err)
	}
	var lastID int64
	count := 0
	more := false
	for rows.Next() {
		if count == limit {
			more = true
			break
		}
		var id int64
		var username string
		if err := rows.Scan(&id, &username); err != nil {
			return 0, fmt.Errorf("read account: %w", err)
		}
		if _, err := fmt.Fprintf(output, "%d\t%q\n", id, username); err != nil {
			return 0, fmt.Errorf("write account listing: %w", err)
		}
		lastID = id
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read accounts: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close account listing: %w", err)
	}
	if more {
		return lastID, nil
	}
	return 0, nil
}
