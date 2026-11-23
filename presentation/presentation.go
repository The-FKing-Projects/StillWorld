// Package presentation defines the optional server-rendered game interface.
package presentation

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/The-FKing-Projects/StillWorld/action"
)

// Form describes one action that can be started from the game page.
type Form struct {
	Action      string
	Label       string
	Description string
	// Cost is an optional display hint for the action's Turns cost. Nil omits
	// the cost line; a non-nil value (including zero) is shown before the
	// button. The action engine's Prepared.Cost remains authoritative.
	Cost    *int64
	Numbers []NumberField
}

// CostOf returns a pointer to n for Form.Cost display.
func CostOf(n int64) *int64 {
	return &n
}

// NumberField describes an integer input. Bounds are browser hints; games must
// validate submitted values in DecodeForm and again in the action handler.
type NumberField struct {
	Name  string
	Label string
	Value int64
	Min   int64
	Max   int64
}

// Section is a text-only portion of the game page.
type Section struct {
	Heading string
	Lines   []string
}

// Page contains game-owned content for an authenticated player.
type Page struct {
	Title       string
	Description string
	Sections    []Section
	Forms       []Form
}

// Game supplies game-specific page content, form input, and result text.
// Player identities are supplied by the framework after authentication.
// DecodeForm must reject unknown or duplicated fields and produce deterministic
// JSON for the same values, so retries preserve the action input fingerprint.
type Game interface {
	Home(context.Context, action.Player) (Page, error)
	DecodeForm(actionName string, fields url.Values) (json.RawMessage, error)
	Result(actionName string, result json.RawMessage) (string, error)
}
