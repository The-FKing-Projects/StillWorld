package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/auth"
	"github.com/The-FKing-Projects/StillWorld/internal/config"
	"github.com/The-FKing-Projects/StillWorld/internal/db"
)

func revokeSessions(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: stillworld revoke-sessions <username>")
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
	svc, err := auth.NewService(database)
	if err != nil {
		fmt.Fprintln(stderr, "stillworld:", err)
		return 1
	}
	if err := svc.RevokeSessions(ctx, args[0]); err != nil {
		fmt.Fprintln(stderr, "stillworld: revoke sessions:", err)
		return 1
	}
	if _, err := fmt.Fprintln(stdout, "Existing sessions revoked; new logins remain possible."); err != nil {
		fmt.Fprintln(stderr, "stillworld: sessions were revoked but confirmation could not be written:", err)
		return 1
	}
	return 0
}
