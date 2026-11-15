-- +goose Up
-- Optional TOTP second factor (issue #15). Seeds are stored encrypted; recovery
-- codes are stored hashed. Login challenges bridge password and TOTP steps.

CREATE TABLE account_totp (
    account_id   BIGINT PRIMARY KEY REFERENCES accounts (id) ON DELETE CASCADE,
    key_id       TEXT        NOT NULL,
    nonce        BYTEA       NOT NULL CHECK (octet_length(nonce) = 12),
    ciphertext   BYTEA       NOT NULL,
    last_step    BIGINT      NOT NULL DEFAULT -1,
    confirmed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE totp_pending (
    account_id BIGINT PRIMARY KEY REFERENCES accounts (id) ON DELETE CASCADE,
    key_id     TEXT        NOT NULL,
    nonce      BYTEA       NOT NULL CHECK (octet_length(nonce) = 12),
    ciphertext BYTEA       NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX totp_pending_expires_at_idx ON totp_pending (expires_at);

CREATE TABLE totp_recovery_codes (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id BIGINT NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    code_hash  BYTEA  NOT NULL,
    used_at    TIMESTAMPTZ,
    UNIQUE (account_id, code_hash)
);

CREATE INDEX totp_recovery_codes_account_id_idx ON totp_recovery_codes (account_id);

CREATE TABLE login_challenges (
    token_hash BYTEA PRIMARY KEY,
    account_id BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX login_challenges_account_id_idx ON login_challenges (account_id);
CREATE INDEX login_challenges_expires_at_idx ON login_challenges (expires_at);

-- +goose Down
DROP TABLE login_challenges;
DROP TABLE totp_recovery_codes;
DROP TABLE totp_pending;
DROP TABLE account_totp;
