package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/The-FKing-Projects/StillWorld/action"
)

const (
	maxSalvage    = 5
	repairCost    = int64(2)
	repairSalvage = 2
)

type salvageAction struct{}

func (salvageAction) Prepare(ctx context.Context, player action.Player, database action.DB, input json.RawMessage) (action.Prepared, error) {
	if err := decodeEmptyInput(input); err != nil {
		return action.Prepared{}, &action.Rejection{
			Code:    "relay.invalid_input",
			Message: "This action does not accept input.",
		}
	}

	var salvage int
	err := database.QueryRowContext(ctx,
		`SELECT COALESCE((SELECT salvage FROM game_relay_players WHERE account_id = $1), 0)`,
		player.ID).Scan(&salvage)
	if err != nil {
		return action.Prepared{}, fmt.Errorf("read player salvage: %w", err)
	}
	if salvage >= maxSalvage {
		return action.Prepared{}, &action.Rejection{
			Code:    "relay.pack_full",
			Message: "Your pack is full. Contribute salvage to the active relay before collecting more.",
		}
	}

	return action.Prepared{
		Cost: 1,
		Apply: func(ctx context.Context, database action.DB) (json.RawMessage, error) {
			var result struct {
				Salvage       int   `json:"salvage"`
				MaxSalvage    int   `json:"max_salvage"`
				TotalSalvaged int64 `json:"total_salvaged"`
			}
			err := database.QueryRowContext(ctx,
				`INSERT INTO game_relay_players (account_id, salvage, total_salvaged)
				 VALUES ($1, 1, 1)
				 ON CONFLICT (account_id) DO UPDATE
				    SET salvage = game_relay_players.salvage + 1,
				        total_salvaged = game_relay_players.total_salvaged + 1
				 RETURNING salvage, total_salvaged`,
				player.ID).Scan(&result.Salvage, &result.TotalSalvaged)
			if err != nil {
				return nil, fmt.Errorf("record salvaged component: %w", err)
			}
			result.MaxSalvage = maxSalvage
			return json.Marshal(result)
		},
	}, nil
}

type repairAction struct{}

func (repairAction) Prepare(ctx context.Context, player action.Player, database action.DB, input json.RawMessage) (action.Prepared, error) {
	if err := decodeEmptyInput(input); err != nil {
		return action.Prepared{}, &action.Rejection{
			Code:    "relay.invalid_input",
			Message: "This action does not accept input.",
		}
	}

	var site relaySite
	err := database.QueryRowContext(ctx,
		`SELECT site_id, name, completed_work, required_work
		   FROM game_relay_sites
		  WHERE active AND NOT complete
		  FOR UPDATE`).Scan(&site.ID, &site.Name, &site.CompletedWork, &site.RequiredWork)
	if errors.Is(err, sql.ErrNoRows) {
		return action.Prepared{}, &action.Rejection{
			Code:    "relay.network_complete",
			Message: "Every relay is online. The network has been restored.",
		}
	}
	if err != nil {
		return action.Prepared{}, fmt.Errorf("lock active relay site: %w", err)
	}

	var salvage int
	err = database.QueryRowContext(ctx,
		`SELECT COALESCE((SELECT salvage FROM game_relay_players WHERE account_id = $1), 0)`,
		player.ID).Scan(&salvage)
	if err != nil {
		return action.Prepared{}, fmt.Errorf("read player salvage: %w", err)
	}
	if salvage < repairSalvage {
		return action.Prepared{}, &action.Rejection{
			Code:    "relay.need_salvage",
			Message: "Collect two spare components before repairing a relay.",
		}
	}

	return action.Prepared{
		Cost: repairCost,
		Apply: func(ctx context.Context, database action.DB) (json.RawMessage, error) {
			var salvageRemaining int
			err := database.QueryRowContext(ctx,
				`UPDATE game_relay_players
				    SET salvage = salvage - $2, repairs = repairs + 1
				  WHERE account_id = $1 AND salvage >= $2
				RETURNING salvage`,
				player.ID, repairSalvage).Scan(&salvageRemaining)
			if err != nil {
				return nil, fmt.Errorf("spend repair salvage: %w", err)
			}

			site.CompletedWork++
			site.Complete = site.CompletedWork == site.RequiredWork
			site.Active = !site.Complete
			err = database.QueryRowContext(ctx,
				`UPDATE game_relay_sites
				    SET completed_work = $2, active = $3, complete = $4
				  WHERE site_id = $1 AND active AND NOT complete
				RETURNING completed_work`,
				site.ID, site.CompletedWork, site.Active, site.Complete).Scan(&site.CompletedWork)
			if err != nil {
				return nil, fmt.Errorf("record relay repair: %w", err)
			}

			campaignComplete := false
			if site.Complete {
				result, err := database.ExecContext(ctx,
					`UPDATE game_relay_sites SET active = TRUE
					  WHERE site_id = $1 AND NOT complete`, site.ID+1)
				if err != nil {
					return nil, fmt.Errorf("activate next relay: %w", err)
				}
				rows, err := result.RowsAffected()
				if err != nil {
					return nil, fmt.Errorf("count activated relay sites: %w", err)
				}
				campaignComplete = rows == 0
			}

			return json.Marshal(struct {
				SiteID           int    `json:"site_id"`
				SiteName         string `json:"site_name"`
				CompletedWork    int    `json:"completed_work"`
				RequiredWork     int    `json:"required_work"`
				SiteComplete     bool   `json:"site_complete"`
				SalvageRemaining int    `json:"salvage_remaining"`
				NetworkComplete  bool   `json:"network_complete"`
			}{
				SiteID:           site.ID,
				SiteName:         site.Name,
				CompletedWork:    site.CompletedWork,
				RequiredWork:     site.RequiredWork,
				SiteComplete:     site.Complete,
				SalvageRemaining: salvageRemaining,
				NetworkComplete:  campaignComplete,
			})
		},
	}, nil
}

type relaySite struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	CompletedWork int    `json:"completed_work"`
	RequiredWork  int    `json:"required_work"`
	Active        bool   `json:"active"`
	Complete      bool   `json:"complete"`
}

type relayWorld struct {
	Sites    []relaySite `json:"sites"`
	Complete bool        `json:"complete"`
}

func readWorld(ctx context.Context, database action.DB) (relayWorld, error) {
	rows, err := database.QueryContext(ctx,
		`SELECT site_id, name, completed_work, required_work, active, complete
		   FROM game_relay_sites
		  ORDER BY site_id`)
	if err != nil {
		return relayWorld{}, fmt.Errorf("read relay sites: %w", err)
	}
	defer rows.Close()

	world := relayWorld{Sites: make([]relaySite, 0, 3), Complete: true}
	for rows.Next() {
		var site relaySite
		if err := rows.Scan(&site.ID, &site.Name, &site.CompletedWork, &site.RequiredWork, &site.Active, &site.Complete); err != nil {
			return relayWorld{}, fmt.Errorf("scan relay site: %w", err)
		}
		world.Sites = append(world.Sites, site)
		world.Complete = world.Complete && site.Complete
	}
	if err := rows.Err(); err != nil {
		return relayWorld{}, fmt.Errorf("iterate relay sites: %w", err)
	}
	if len(world.Sites) != 3 {
		return relayWorld{}, fmt.Errorf("relay world has %d sites, want 3", len(world.Sites))
	}
	return world, nil
}

func decodeEmptyInput(input json.RawMessage) error {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("expected an empty JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var request struct{}
	if err := decoder.Decode(&request); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}
