-- +goose Up
CREATE TABLE game_counters (
    account_id BIGINT NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    value      BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT game_counters_account_id_key PRIMARY KEY (account_id),
    CONSTRAINT game_counters_value_nonnegative CHECK (value >= 0)
);

-- +goose Down
DROP TABLE game_counters;
