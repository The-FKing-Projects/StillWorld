-- +goose Up
CREATE TABLE accounts (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username         TEXT        NOT NULL CHECK (length(username) BETWEEN 1 AND 64),
    email            TEXT        CHECK (email IS NULL OR length(email) <= 254),
    password_hash    TEXT        NOT NULL,
    turns_balance    NUMERIC     NOT NULL DEFAULT 0 CHECK (turns_balance >= 0),
    turns_updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX accounts_username_lower_key ON accounts (lower(username));
CREATE UNIQUE INDEX accounts_email_lower_key ON accounts (lower(email)) WHERE email IS NOT NULL;

-- Only a hash of the session token is stored, never the token itself.
CREATE TABLE sessions (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_hash   BYTEA       NOT NULL UNIQUE,
    account_id   BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL
);

CREATE INDEX sessions_account_id_idx ON sessions (account_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

-- +goose Down
DROP TABLE sessions;
DROP TABLE accounts;
