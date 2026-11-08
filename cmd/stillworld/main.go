// Command stillworld is the entry point for the StillWorld server.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/auth"
	"github.com/The-FKing-Projects/StillWorld/internal/config"
	"github.com/The-FKing-Projects/StillWorld/internal/db"
	"github.com/The-FKing-Projects/StillWorld/internal/web"
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
	case "serve":
		return serve(stderr)
	case "":
		fmt.Fprintln(stderr, "stillworld: available commands: migrate, serve")
		return 1
	default:
		fmt.Fprintf(stderr, "stillworld: unknown command %q\n", fs.Arg(0))
		return 2
	}
}

func serve(stderr io.Writer) int {
	cfg, err := config.FromEnv()
	if err != nil {
		fmt.Fprintln(stderr, "stillworld:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(stderr, "stillworld:", err)
		return 1
	}
	defer d.Close()

	svc, err := auth.NewService(d)
	if err != nil {
		fmt.Fprintln(stderr, "stillworld:", err)
		return 1
	}
	go cleanupSessions(ctx, svc, stderr)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           web.New(svc, web.Options{InsecureCookies: cfg.InsecureCookies, BehindProxy: cfg.BehindProxy}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	fmt.Fprintln(stderr, "stillworld: listening on", cfg.ListenAddr)

	select {
	case err := <-errc:
		fmt.Fprintln(stderr, "stillworld:", err)
		return 1
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintln(stderr, "stillworld: shutdown:", err)
		return 1
	}
	return 0
}

func cleanupSessions(ctx context.Context, svc *auth.Service, stderr io.Writer) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := svc.DeleteExpiredSessions(ctx); err != nil {
				fmt.Fprintln(stderr, "stillworld: session cleanup:", err)
			}
		}
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
