-- +goose Up
CREATE TABLE stillworld_action_outcomes (
    account_id         BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    nonce              BYTEA       NOT NULL CHECK (octet_length(nonce) = 16),
    action_name        TEXT        NOT NULL CHECK (length(action_name) BETWEEN 1 AND 128),
    input_fingerprint  BYTEA       NOT NULL CHECK (octet_length(input_fingerprint) = 32),
    expires_at         TIMESTAMPTZ NOT NULL,
    outcome_version    SMALLINT    NOT NULL CHECK (outcome_version = 1),
    status             TEXT        NOT NULL CHECK (status IN ('success', 'rejected')),
    result_json        BYTEA,
    rejection_code     TEXT,
    rejection_message  TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (account_id, nonce),
    CHECK (result_json IS NULL OR octet_length(result_json) BETWEEN 1 AND 65536),
    CHECK (rejection_code IS NULL OR length(rejection_code) BETWEEN 1 AND 128),
    CHECK (rejection_message IS NULL OR octet_length(rejection_message) BETWEEN 1 AND 1024),
    CHECK (
        (status = 'success' AND result_json IS NOT NULL AND rejection_code IS NULL AND rejection_message IS NULL)
        OR
        (status = 'rejected' AND result_json IS NULL AND rejection_code IS NOT NULL AND rejection_message IS NOT NULL)
    )
);

CREATE INDEX stillworld_action_outcomes_expires_at
    ON stillworld_action_outcomes (expires_at);

-- +goose Down
DROP TABLE stillworld_action_outcomes;
