package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/The-FKing-Projects/StillWorld"
	"github.com/The-FKing-Projects/StillWorld/action"
	"github.com/The-FKing-Projects/StillWorld/recovery"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "relay:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	command := "serve"
	if len(args) > 1 {
		return errors.New("usage: relay [migrate|serve]")
	}
	if len(args) == 1 {
		command = args[0]
	}
	if command != "migrate" && command != "serve" {
		return fmt.Errorf("unknown command %q; usage: relay [migrate|serve]", command)
	}

	databaseURL := os.Getenv("STILLWORLD_DATABASE_URL")
	if databaseURL == "" {
		return errors.New("STILLWORLD_DATABASE_URL is required")
	}
	key, err := hex.DecodeString(os.Getenv("RELAY_REQUEST_KEY"))
	if err != nil || len(key) < 32 {
		return errors.New("RELAY_REQUEST_KEY must be at least 32 bytes encoded as hexadecimal")
	}
	recoveryConfig, err := recovery.FromEnv()
	if err != nil {
		return err
	}
	insecureCookies, err := boolEnv("STILLWORLD_INSECURE_COOKIES")
	if err != nil {
		return err
	}
	behindProxy, err := boolEnv("STILLWORLD_BEHIND_PROXY")
	if err != nil {
		return err
	}
	migrations, err := fs.Sub(embeddedMigrations, "migrations")
	if err != nil {
		return fmt.Errorf("locate relay migrations: %w", err)
	}

	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()
	database.SetMaxOpenConns(10)

	app, err := newApplication(database, migrations, key, insecureCookies, behindProxy, recoveryConfig)
	if err != nil {
		return fmt.Errorf("configure StillWorld: %w", err)
	}
	if command == "migrate" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := app.Migrate(ctx); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		fmt.Fprintln(stdout, "relay: migrations applied")
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	appHandler, err := app.Handler(ctx)
	if err != nil {
		return fmt.Errorf("check schema readiness: %w", err)
	}
	go cleanupOutcomes(ctx, app, stderr)
	server := &http.Server{
		Addr:              envOr("STILLWORLD_LISTEN_ADDR", "127.0.0.1:8082"),
		Handler:           newHTTPHandler(database, appHandler, stderr),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	errc := make(chan error, 1)
	go func() { errc <- server.ListenAndServe() }()
	fmt.Fprintln(stderr, "relay: listening on", server.Addr)
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

func newApplication(database *sql.DB, migrations fs.FS, key []byte, insecureCookies, behindProxy bool, recoveryConfig *recovery.Config) (*stillworld.App, error) {
	return stillworld.New(database, stillworld.Game{
		ID:         "relay",
		Migrations: migrations,
		Actions: []stillworld.Registration{
			{Name: "relay.salvage", Handler: salvageAction{}},
			{Name: "relay.repair", Handler: repairAction{}},
		},
	}, stillworld.Options{
		Turns:                 stillworld.TurnsPolicy{Cap: 20, Interval: 30 * time.Minute},
		RequestTokenActiveKey: "v1",
		RequestTokenKeys:      map[string][]byte{"v1": key},
		InsecureCookies:       insecureCookies,
		BehindProxy:           behindProxy,
		Presentation:          relayPresentation{database: database},
		Recovery:              recoveryConfig,
	})
}

func newHTTPHandler(database action.DB, appHandler http.Handler, stderr io.Writer) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/world", func(w http.ResponseWriter, r *http.Request) {
		setJSONHeaders(w)
		world, err := readWorld(r.Context(), database)
		if err != nil {
			fmt.Fprintln(stderr, "relay: read world:", err)
			w.WriteHeader(http.StatusInternalServerError)
			if err := json.NewEncoder(w).Encode(struct {
				Error string `json:"error"`
			}{Error: "world_unavailable"}); err != nil {
				fmt.Fprintln(stderr, "relay: encode world error:", err)
			}
			return
		}
		if err := json.NewEncoder(w).Encode(world); err != nil {
			fmt.Fprintln(stderr, "relay: encode world:", err)
		}
	})
	mux.Handle("/", appHandler)
	return mux
}

func setJSONHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func cleanupOutcomes(ctx context.Context, app *stillworld.App, stderr io.Writer) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := app.CleanupExpiredOutcomes(ctx, 100); err != nil {
				fmt.Fprintln(stderr, "relay: clean expired action outcomes:", err)
			}
		}
	}
}

func boolEnv(name string) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", name, err)
	}
	return parsed, nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
