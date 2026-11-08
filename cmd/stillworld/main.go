// Command stillworld is the entry point for the StillWorld server.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/The-FKing-Projects/StillWorld/internal/config"
	"github.com/The-FKing-Projects/StillWorld/internal/db"
)

const version = "0.0.0-dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stillworld", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *showVersion {
		fmt.Fprintln(stdout, "stillworld", version)
		return 0
	}

	switch fs.Arg(0) {
	case "migrate":
		return migrate(stderr)
	case "":
		fmt.Fprintln(stderr, "stillworld: no server implemented yet; available commands: migrate")
		return 1
	default:
		fmt.Fprintf(stderr, "stillworld: unknown command %q\n", fs.Arg(0))
		return 2
	}
}

func migrate(stderr io.Writer) int {
	cfg, err := config.FromEnv()
	if err != nil {
		fmt.Fprintln(stderr, "stillworld:", err)
		return 1
	}
	ctx := context.Background()
	d, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(stderr, "stillworld:", err)
		return 1
	}
	defer d.Close()
	if err := db.Migrate(ctx, d); err != nil {
		fmt.Fprintln(stderr, "stillworld:", err)
		return 1
	}
	return 0
}
