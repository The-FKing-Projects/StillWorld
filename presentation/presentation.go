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
	Numbers     []NumberField
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
