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

func changeAccountStatus(args []string, enabled bool, stdout, stderr io.Writer) int {
	command := "disable-account"
	if enabled {
		command = "enable-account"
	}
	if len(args) != 1 {
		fmt.Fprintf(stderr, "usage: stillworld %s <username>\n", command)
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
	if err := svc.SetAccountEnabled(ctx, args[0], enabled); err != nil {
		fmt.Fprintln(stderr, "stillworld: change account status:", err)
		return 1
	}
	if enabled {
		fmt.Fprintln(stdout, "Account enabled; revoked sessions are not restored.")
	} else {
		fmt.Fprintln(stdout, "Account disabled; existing sessions revoked.")
	}
	return 0
}
