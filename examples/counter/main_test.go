package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/The-FKing-Projects/StillWorld/action"
)

func TestIncrementActionPreparation(t *testing.T) {
	prepared, err := (incrementAction{}).Prepare(
		context.Background(),
		action.Player{ID: 42},
		nil,
		json.RawMessage(`{"amount":3}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Cost != 3 || prepared.Apply == nil {
		t.Fatalf("prepared action = %+v", prepared)
	}
}

func TestIncrementActionRejectsInvalidInput(t *testing.T) {
	for _, input := range []string{`{"amount":0}`, `{"amount":101}`, `{"amount":1,"extra":true}`} {
		_, err := (incrementAction{}).Prepare(
			context.Background(),
			action.Player{ID: 42},
			nil,
			json.RawMessage(input),
		)
		rejection, ok := err.(*action.Rejection)
		if !ok || rejection.Code == "" {
			t.Errorf("input %s error = %v, want a typed rejection", input, err)
		}
	}
}
