// Package actionregistry validates and indexes game-registered actions.
package actionregistry

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/The-FKing-Projects/StillWorld/action"
	"github.com/The-FKing-Projects/StillWorld/internal/actionname"
)

var (
	ErrInvalidGameID     = errors.New("action registry: invalid game ID")
	ErrInvalidActionName = errors.New("action registry: invalid action name")
	ErrMissingHandler    = errors.New("action registry: missing handler")
	ErrDuplicateAction   = errors.New("action registry: duplicate action name")
)

// Registration binds a stable action name to its implementation.
type Registration struct {
	Name    string
	Handler action.Action
}

// Registry is an immutable mapping of action names to shared handlers.
type Registry struct {
	gameID  string
	actions map[string]action.Action
}

// New validates game identity and all action registrations.
func New(gameID string, registrations []Registration) (*Registry, error) {
	if !actionname.ValidGameID(gameID) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidGameID, gameID)
	}
	actions := make(map[string]action.Action, len(registrations))
	for _, registration := range registrations {
		if !actionname.ValidActionName(registration.Name) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidActionName, registration.Name)
		}
		if nilHandler(registration.Handler) {
			return nil, fmt.Errorf("%w: %q", ErrMissingHandler, registration.Name)
		}
		if _, exists := actions[registration.Name]; exists {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateAction, registration.Name)
		}
		actions[registration.Name] = registration.Handler
	}
	return &Registry{gameID: gameID, actions: actions}, nil
}

// GameID returns the validated game identifier.
func (r *Registry) GameID() string {
	if r == nil {
		return ""
	}
	return r.gameID
}

// Lookup returns the registered handler for name.
func (r *Registry) Lookup(name string) (action.Action, bool) {
	if r == nil {
		return nil, false
	}
	handler, ok := r.actions[name]
	return handler, ok
}

func nilHandler(handler action.Action) bool {
	if handler == nil {
		return true
	}
	value := reflect.ValueOf(handler)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
