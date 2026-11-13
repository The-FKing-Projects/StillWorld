-- +goose Up
CREATE TABLE stillworld_deployment (
    id         SMALLINT PRIMARY KEY CHECK (id = 1),
    game_id    TEXT        NOT NULL CHECK (game_id ~ '^[a-z][a-z0-9_]{0,31}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

-- +goose Down
DROP TABLE stillworld_deployment;
