-- +goose Up
CREATE TABLE pending_signups (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username TEXT NOT NULL CHECK (length(username) BETWEEN 1 AND 64),
    email TEXT NOT NULL CHECK (length(email) BETWEEN 1 AND 254),
    password_hash TEXT NOT NULL,
    token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX pending_signups_username_lower_key ON pending_signups (lower(username));
CREATE UNIQUE INDEX pending_signups_email_lower_key ON pending_signups (lower(email));
CREATE INDEX pending_signups_expires_at_idx ON pending_signups (expires_at);

-- +goose Down
DROP TABLE pending_signups;
