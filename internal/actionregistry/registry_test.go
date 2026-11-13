package actionregistry

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/The-FKing-Projects/StillWorld/action"
)

type testAction struct{}

func (*testAction) Prepare(context.Context, action.Player, action.DB, json.RawMessage) (action.Prepared, error) {
	return action.Prepared{}, nil
}

func TestNewAndLookup(t *testing.T) {
	handler := &testAction{}
	registry, err := New("counter_game", []Registration{
		{Name: "counter.increment", Handler: handler},
	})
	if err != nil {
		t.Fatal(err)
	}
	if registry.GameID() != "counter_game" {
		t.Fatalf("GameID() = %q", registry.GameID())
	}
	got, ok := registry.Lookup("counter.increment")
	if !ok || got != handler {
		t.Fatal("registered handler was not returned")
	}
	if _, ok := registry.Lookup("counter.reset"); ok {
		t.Fatal("unregistered handler was returned")
	}
}

func TestNewRejectsInvalidRegistrations(t *testing.T) {
	handler := &testAction{}
	var nilHandler *testAction
	tests := []struct {
		name          string
		gameID        string
		registrations []Registration
		want          error
	}{
		{name: "invalid game ID", gameID: "Counter", want: ErrInvalidGameID},
		{name: "invalid action name", gameID: "counter", registrations: []Registration{{Name: "counter..inc", Handler: handler}}, want: ErrInvalidActionName},
		{name: "missing handler", gameID: "counter", registrations: []Registration{{Name: "counter.inc"}}, want: ErrMissingHandler},
		{name: "typed nil handler", gameID: "counter", registrations: []Registration{{Name: "counter.inc", Handler: nilHandler}}, want: ErrMissingHandler},
		{name: "duplicate action", gameID: "counter", registrations: []Registration{
			{Name: "counter.inc", Handler: handler},
			{Name: "counter.inc", Handler: handler},
		}, want: ErrDuplicateAction},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.gameID, tt.registrations)
			if !errors.Is(err, tt.want) {
				t.Fatalf("New() error = %v, want %v", err, tt.want)
			}
		})
	}
}
