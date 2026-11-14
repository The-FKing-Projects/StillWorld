-- +goose Up
CREATE TABLE password_reset_tokens (
    account_id BIGINT PRIMARY KEY REFERENCES accounts (id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    expires_at TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE password_reset_tokens;
