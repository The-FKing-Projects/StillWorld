-- +goose Up
-- Optional WebAuthn passkeys as a second factor (issue #16, decision A).
-- Credential rows store public verifier material only. Ceremonies hold
-- short-lived go-webauthn session JSON between begin and finish.

CREATE TABLE webauthn_credentials (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id      BIGINT NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    credential_id   BYTEA NOT NULL,
    credential_json JSONB NOT NULL,
    label           TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at    TIMESTAMPTZ,
    UNIQUE (credential_id)
);

CREATE INDEX webauthn_credentials_account_id_idx ON webauthn_credentials (account_id);

CREATE TABLE webauthn_register_pending (
    account_id   BIGINT PRIMARY KEY REFERENCES accounts (id) ON DELETE CASCADE,
    session_data JSONB NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL
);

CREATE INDEX webauthn_register_pending_expires_at_idx ON webauthn_register_pending (expires_at);

CREATE TABLE webauthn_assert_pending (
    login_challenge_hash BYTEA PRIMARY KEY REFERENCES login_challenges (token_hash) ON DELETE CASCADE,
    account_id           BIGINT NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    session_data         JSONB NOT NULL,
    expires_at           TIMESTAMPTZ NOT NULL
);

CREATE INDEX webauthn_assert_pending_expires_at_idx ON webauthn_assert_pending (expires_at);

-- +goose Down
DROP TABLE webauthn_assert_pending;
DROP TABLE webauthn_register_pending;
DROP TABLE webauthn_credentials;
