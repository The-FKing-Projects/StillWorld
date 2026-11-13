package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/The-FKing-Projects/StillWorld/action"
	"github.com/The-FKing-Projects/StillWorld/presentation"
)

type relayPresentation struct {
	database *sql.DB
}

func (p relayPresentation) Home(ctx context.Context, player action.Player) (presentation.Page, error) {
	world, err := readWorld(ctx, p.database)
	if err != nil {
		return presentation.Page{}, err
	}
	var salvage int
	err = p.database.QueryRowContext(ctx,
		`SELECT COALESCE((SELECT salvage FROM game_relay_players WHERE account_id = $1), 0)`,
		player.ID).Scan(&salvage)
	if err != nil {
		return presentation.Page{}, fmt.Errorf("read salvage: %w", err)
	}
	network := presentation.Section{Heading: "Relay network"}
	for _, site := range world.Sites {
		state := "Waiting"
		if site.Active {
			state = "Active"
		} else if site.Complete {
			state = "Online"
		}
		network.Lines = append(network.Lines, fmt.Sprintf("%s — %s, %d of %d repairs", site.Name, state, site.CompletedWork, site.RequiredWork))
	}
	page := presentation.Page{
		Title:       "Restore the relay network",
		Description: "Work together to collect components and bring the signal stations online.",
		Sections: []presentation.Section{
			network,
			{Heading: "Your pack", Lines: []string{fmt.Sprintf("%d of %d components", salvage, maxSalvage)}},
		},
	}
	if world.Complete {
		page.Description = "The network is online. All three stations have been restored."
		return page, nil
	}
	page.Forms = []presentation.Form{
		{Action: "relay.salvage", Label: "Collect a component", Description: "Spend 1 Turn to collect one component. Your pack holds up to five."},
		{Action: "relay.repair", Label: "Repair the active station", Description: "Spend 2 Turns and 2 components to make one repair."},
	}
	return page, nil
}

func (relayPresentation) DecodeForm(name string, fields url.Values) (json.RawMessage, error) {
	if (name != "relay.salvage" && name != "relay.repair") || len(fields) != 0 {
		return nil, errors.New("invalid Relay form")
	}
	return json.RawMessage(`{}`), nil
}

func (relayPresentation) Result(name string, result json.RawMessage) (string, error) {
	switch name {
	case "relay.salvage":
		var value struct {
			Salvage    int `json:"salvage"`
			MaxSalvage int `json:"max_salvage"`
		}
		if err := json.Unmarshal(result, &value); err != nil {
			return "", err
		}
		return fmt.Sprintf("You collected a component. Your pack now has %d of %d.", value.Salvage, value.MaxSalvage), nil
	case "relay.repair":
		var value struct {
			SiteName        string `json:"site_name"`
			CompletedWork   int    `json:"completed_work"`
			RequiredWork    int    `json:"required_work"`
			SiteComplete    bool   `json:"site_complete"`
			NetworkComplete bool   `json:"network_complete"`
		}
		if err := json.Unmarshal(result, &value); err != nil {
			return "", err
		}
		if value.NetworkComplete {
			return "Your repair brought the final station online. The network is restored.", nil
		}
		if value.SiteComplete {
			return fmt.Sprintf("Your repair brought %s online. The next station is ready.", value.SiteName), nil
		}
		return fmt.Sprintf("You repaired %s: %d of %d repairs completed.", value.SiteName, value.CompletedWork, value.RequiredWork), nil
	default:
		return "", errors.New("unknown Relay action result")
	}
}
