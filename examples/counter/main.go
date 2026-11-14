package main

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
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
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	if len(os.Args) > 2 || (len(os.Args) == 2 && os.Args[1] != "migrate" && os.Args[1] != "serve") {
		return errors.New("usage: counter [migrate|serve]")
	}
	databaseURL := os.Getenv("STILLWORLD_DATABASE_URL")
	if databaseURL == "" {
		return errors.New("STILLWORLD_DATABASE_URL is required")
	}
	key, err := hex.DecodeString(os.Getenv("COUNTER_REQUEST_KEY"))
	if err != nil || len(key) < 32 {
		return errors.New("COUNTER_REQUEST_KEY must be at least 32 bytes encoded as hexadecimal")
	}
	recoveryConfig, err := recovery.FromEnv()
	if err != nil {
		return err
	}
	migrations, err := fs.Sub(embeddedMigrations, "migrations")
	if err != nil {
		return fmt.Errorf("locate counter migrations: %w", err)
	}

	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()
	database.SetMaxOpenConns(10)

	app, err := stillworld.New(database, stillworld.Game{
		ID:         "counter",
		Migrations: migrations,
		Actions: []stillworld.Registration{
			{Name: "counter.increment", Handler: incrementAction{}},
		},
	}, stillworld.Options{
		Turns:                 stillworld.TurnsPolicy{Cap: 100, Interval: time.Hour},
		RequestTokenActiveKey: "v1",
		RequestTokenKeys:      map[string][]byte{"v1": key},
		InsecureCookies:       true,
		Presentation:          counterPresentation{database: database},
		Recovery:              recoveryConfig,
	})
	if err != nil {
		return fmt.Errorf("configure StillWorld: %w", err)
	}

	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := app.Migrate(ctx); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	handler, err := app.Handler(ctx)
	if err != nil {
		return fmt.Errorf("check schema readiness: %w", err)
	}
	go cleanupOutcomes(ctx, app)
	server := &http.Server{
		Addr:              envOr("STILLWORLD_LISTEN_ADDR", "127.0.0.1:8081"),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	errc := make(chan error, 1)
	go func() { errc <- server.ListenAndServe() }()
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

func cleanupOutcomes(ctx context.Context, app *stillworld.App) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := app.CleanupExpiredOutcomes(ctx, 100); err != nil {
				log.Printf("clean expired action outcomes: %v", err)
			}
		}
	}
}

type incrementAction struct{}

func (incrementAction) Prepare(_ context.Context, player action.Player, _ action.DB, input json.RawMessage) (action.Prepared, error) {
	var request struct {
		Amount int64 `json:"amount"`
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return action.Prepared{}, &action.Rejection{
			Code:    "counter.invalid_input",
			Message: "Provide a positive increment amount.",
		}
	}
	if request.Amount < 1 || request.Amount > 100 {
		return action.Prepared{}, &action.Rejection{
			Code:    "counter.invalid_amount",
			Message: "The increment amount must be between 1 and 100.",
		}
	}
	return action.Prepared{
		Cost: request.Amount,
		Apply: func(ctx context.Context, database action.DB) (json.RawMessage, error) {
			var value int64
			err := database.QueryRowContext(ctx,
				`INSERT INTO game_counters (account_id, value)
				 VALUES ($1, $2)
				 ON CONFLICT (account_id) DO UPDATE
				    SET value = game_counters.value + EXCLUDED.value
				 RETURNING value`,
				player.ID, request.Amount).Scan(&value)
			if err != nil {
				return nil, fmt.Errorf("increment counter: %w", err)
			}
			result, err := json.Marshal(struct {
				Value int64 `json:"value"`
			}{Value: value})
			return json.RawMessage(result), err
		},
	}, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
