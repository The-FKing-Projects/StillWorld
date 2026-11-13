package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"github.com/The-FKing-Projects/StillWorld/action"
	"github.com/The-FKing-Projects/StillWorld/presentation"
)

type counterPresentation struct {
	database *sql.DB
}

func (p counterPresentation) Home(ctx context.Context, player action.Player) (presentation.Page, error) {
	var value int64
	err := p.database.QueryRowContext(ctx,
		`SELECT COALESCE((SELECT value FROM game_counters WHERE account_id = $1), 0)`,
		player.ID).Scan(&value)
	if err != nil {
		return presentation.Page{}, fmt.Errorf("read counter: %w", err)
	}
	return presentation.Page{
		Title:       "Counter example",
		Description: "Increase your counter by spending Turns.",
		Sections: []presentation.Section{
			{Heading: "Your counter", Lines: []string{fmt.Sprintf("Current value: %d", value)}},
		},
		Forms: []presentation.Form{{
			Action:      "counter.increment",
			Label:       "Increase counter",
			Description: "Each increment costs one Turn.",
			Numbers: []presentation.NumberField{
				{Name: "amount", Label: "Amount", Value: 1, Min: 1, Max: 100},
			},
		}},
	}, nil
}

func (counterPresentation) DecodeForm(name string, fields url.Values) (json.RawMessage, error) {
	if name != "counter.increment" || len(fields) != 1 || len(fields["amount"]) != 1 {
		return nil, errors.New("invalid counter form")
	}
	amount, err := strconv.ParseInt(fields.Get("amount"), 10, 64)
	if err != nil || amount < 1 || amount > 100 {
		return nil, errors.New("invalid increment amount")
	}
	return json.Marshal(struct {
		Amount int64 `json:"amount"`
	}{Amount: amount})
}

func (counterPresentation) Result(name string, result json.RawMessage) (string, error) {
	if name != "counter.increment" {
		return "", errors.New("unknown counter action result")
	}
	var value struct {
		Value int64 `json:"value"`
	}
	if err := json.Unmarshal(result, &value); err != nil {
		return "", err
	}
	return fmt.Sprintf("Your counter is now %d.", value.Value), nil
}
