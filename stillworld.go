// Package stillworld composes an authenticated StillWorld application for one
// game. Game code depends on this package and action, never on internal APIs.
package stillworld

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"net/http"
	"time"

	"github.com/The-FKing-Projects/StillWorld/action"
	"github.com/The-FKing-Projects/StillWorld/internal/actionengine"
	"github.com/The-FKing-Projects/StillWorld/internal/actionregistry"
	"github.com/The-FKing-Projects/StillWorld/internal/actiontoken"
	"github.com/The-FKing-Projects/StillWorld/internal/auth"
	"github.com/The-FKing-Projects/StillWorld/internal/db"
	"github.com/The-FKing-Projects/StillWorld/internal/turns"
	"github.com/The-FKing-Projects/StillWorld/internal/web"
	"github.com/The-FKing-Projects/StillWorld/presentation"
	"github.com/The-FKing-Projects/StillWorld/recovery"
)

// Registration binds a stable game action name to its implementation.
type Registration struct {
	Name    string
	Handler action.Action
}

// Game describes the one game installed in a StillWorld deployment.
type Game struct {
	ID         string
	Migrations fs.FS
	Actions    []Registration
}

// TurnsPolicy configures per-player regeneration for this game.
type TurnsPolicy struct {
	Cap      int64
	Interval time.Duration
}

// Options configure the application and its request-token signing keys.
type Options struct {
	Turns TurnsPolicy

	RequestTokenActiveKey string
	RequestTokenKeys      map[string][]byte

	InsecureCookies bool
	BehindProxy     bool
	Now             func() time.Time
	// Presentation enables the optional server-rendered game interface.
	Presentation presentation.Game
	// Recovery enables opt-in self-service password recovery.
	Recovery *recovery.Config
	// SharedRateLimits stores authentication rate limits in the application database.
	SharedRateLimits bool
}

// App owns the framework services for one database and one game.
type App struct {
	database   *sql.DB
	gameID     string
	http       web.Options
	auth       *auth.Service
	action     *actionengine.Engine
	migrations fs.FS
}

// New validates application configuration without applying migrations or
// starting a listener. The caller retains ownership of database.
func New(database *sql.DB, game Game, options Options) (*App, error) {
	if database == nil {
		return nil, errors.New("stillworld: database is required")
	}
	if game.Migrations == nil {
		return nil, errors.New("stillworld: game migrations are required")
	}
	if options.Recovery != nil {
		if err := options.Recovery.ValidateForCookies(options.InsecureCookies); err != nil {
			return nil, err
		}
	}
	keyRing, err := actiontoken.NewKeyRing(options.RequestTokenActiveKey, options.RequestTokenKeys)
	if err != nil {
		return nil, err
	}
	registrations := make([]actionregistry.Registration, len(game.Actions))
	for i, registration := range game.Actions {
		registrations[i] = actionregistry.Registration{
			Name:    registration.Name,
			Handler: registration.Handler,
		}
	}
	engine, err := actionengine.New(database, game.ID, turns.Policy{
		Cap:      options.Turns.Cap,
		Interval: options.Turns.Interval,
	}, keyRing, registrations)
	if err != nil {
		return nil, err
	}
	authService, err := auth.NewServiceWithInitialTurns(database, options.Turns.Cap)
	if err != nil {
		return nil, err
	}
	if options.Now != nil {
		authService.Now = options.Now
	}
	httpOptions := web.Options{InsecureCookies: options.InsecureCookies, BehindProxy: options.BehindProxy, Now: options.Now, Presentation: options.Presentation, Recovery: options.Recovery}
	if options.SharedRateLimits {
		httpOptions.SharedRateLimits = database
	}
	return &App{
		database:   database,
		gameID:     game.ID,
		http:       httpOptions,
		auth:       authService,
		action:     engine,
		migrations: game.Migrations,
	}, nil
}

// Migrate applies framework migrations followed by this game's migrations and
// binds an uninitialized schema to the game's ID.
func (app *App) Migrate(ctx context.Context) error {
	if app == nil {
		return errors.New("stillworld: application is required")
	}
	return db.MigrateGameDeployment(ctx, app.database, app.gameID, app.migrations)
}

// CleanupExpiredOutcomes removes expired request outcomes for up to
// batchSize accounts. Call periodically from the game's maintenance worker;
// batchSize must be between 1 and 1000.
func (app *App) CleanupExpiredOutcomes(ctx context.Context, batchSize int) (int64, error) {
	if app == nil || app.action == nil {
		return 0, errors.New("stillworld: application is required")
	}
	return app.action.CleanupExpired(ctx, batchSize)
}

// Handler verifies schema readiness before returning the authenticated HTTP
// handler. It never applies migrations implicitly.
func (app *App) Handler(ctx context.Context) (http.Handler, error) {
	if app == nil {
		return nil, errors.New("stillworld: application is required")
	}
	if err := db.CheckGameDeploymentReady(ctx, app.database, app.gameID, app.migrations); err != nil {
		return nil, err
	}
	opts := app.http
	opts.Actions = app.action
	return web.New(app.auth, opts).Handler(), nil
}
