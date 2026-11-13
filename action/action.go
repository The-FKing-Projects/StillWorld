// Package action defines the public contract between StillWorld and game code.
package action

import (
	"context"
	"database/sql"
	"encoding/json"
)

// Player is the authenticated player performing an action.
type Player struct {
	ID int64
}

// DB provides transaction-scoped database access to an action.
// Implementations do not expose transaction commit or rollback methods.
type DB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Action validates input and prepares its effects within the action transaction.
type Action interface {
	Prepare(context.Context, Player, DB, json.RawMessage) (Prepared, error)
}

// Prepared contains the action cost and transaction-local effects for one request.
type Prepared struct {
	Cost  int64
	Apply func(context.Context, DB) (json.RawMessage, error)
}

// Rejection is a safe, final game-rule outcome for an action request.
type Rejection struct {
	Code    string
	Message string
}

// Error returns the player-safe rejection message.
func (r *Rejection) Error() string {
	if r == nil {
		return ""
	}
	return r.Message
}
