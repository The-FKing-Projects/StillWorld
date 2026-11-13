-- +goose Up
CREATE TABLE game_relay_sites (
    site_id          INTEGER PRIMARY KEY CHECK (site_id BETWEEN 1 AND 3),
    name             TEXT NOT NULL UNIQUE,
    required_work    INTEGER NOT NULL CHECK (required_work > 0),
    completed_work   INTEGER NOT NULL DEFAULT 0,
    active           BOOLEAN NOT NULL DEFAULT FALSE,
    complete         BOOLEAN NOT NULL DEFAULT FALSE,
    CHECK (completed_work BETWEEN 0 AND required_work),
    CHECK (NOT complete OR completed_work = required_work),
    CHECK (NOT active OR NOT complete)
);

CREATE UNIQUE INDEX game_one_active_relay_site
    ON game_relay_sites (active)
    WHERE active;

CREATE TABLE game_relay_players (
    account_id       BIGINT PRIMARY KEY REFERENCES accounts (id) ON DELETE CASCADE,
    salvage          INTEGER NOT NULL DEFAULT 0 CHECK (salvage BETWEEN 0 AND 5),
    total_salvaged   BIGINT NOT NULL DEFAULT 0 CHECK (total_salvaged >= 0),
    repairs          BIGINT NOT NULL DEFAULT 0 CHECK (repairs >= 0)
);

INSERT INTO game_relay_sites (site_id, name, required_work, active)
VALUES
    (1, 'North Shoal', 3, TRUE),
    (2, 'Windbreak Point', 3, FALSE),
    (3, 'Far Lantern', 3, FALSE);

-- +goose Down
DROP TABLE game_relay_players;
DROP TABLE game_relay_sites;
