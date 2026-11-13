package main

import (
	"context"
	"encoding/json"
	"net/url"
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

func TestCounterFormDecoding(t *testing.T) {
	game := counterPresentation{}
	for _, tc := range []struct {
		name   string
		fields url.Values
		want   string
	}{
		{name: "counter.increment", fields: url.Values{"amount": {"3"}}, want: `{"amount":3}`},
		{name: "counter.increment", fields: url.Values{"amount": {"003"}}, want: `{"amount":3}`},
		{name: "counter.increment", fields: url.Values{"amount": {"0"}}},
		{name: "counter.increment", fields: url.Values{"amount": {"101"}}},
		{name: "counter.increment", fields: url.Values{"amount": {"1.5"}}},
		{name: "counter.increment", fields: url.Values{"amount": {"999999999999999999999999"}}},
		{name: "counter.increment", fields: url.Values{"amount": {"1", "2"}}},
		{name: "counter.increment", fields: url.Values{"amount": {"1"}, "extra": {"2"}}},
		{name: "counter.increment", fields: url.Values{}},
		{name: "counter.unknown", fields: url.Values{"amount": {"1"}}},
	} {
		got, err := game.DecodeForm(tc.name, tc.fields)
		if tc.want == "" {
			if err == nil {
				t.Errorf("accepted %s %v", tc.name, tc.fields)
			}
		} else if err != nil || string(got) != tc.want {
			t.Errorf("DecodeForm(%s, %v) = %s, %v; want %s", tc.name, tc.fields, got, err, tc.want)
		}
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
