-- +goose Up
CREATE SEQUENCE stillworld_rate_limit_generation;

CREATE TABLE stillworld_rate_limits (
    scope SMALLINT NOT NULL,
    key_hash BYTEA NOT NULL CHECK (octet_length(key_hash) = 32),
    generation BIGINT NOT NULL DEFAULT nextval('stillworld_rate_limit_generation'),
    failures INTEGER NOT NULL CHECK (failures >= 0),
    pending INTEGER NOT NULL CHECK (pending >= 0),
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (scope, key_hash)
);

CREATE INDEX stillworld_rate_limits_expiry ON stillworld_rate_limits (expires_at);

CREATE TABLE stillworld_rate_limit_capacity (
    singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
    entry_count INTEGER NOT NULL CHECK (entry_count >= 0)
);

INSERT INTO stillworld_rate_limit_capacity (entry_count) VALUES (0);

-- +goose Down
DROP TABLE stillworld_rate_limit_capacity;
DROP TABLE stillworld_rate_limits;
DROP SEQUENCE stillworld_rate_limit_generation;
