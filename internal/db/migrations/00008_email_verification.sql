-- +goose Up
ALTER TABLE accounts ADD COLUMN email_verified_at TIMESTAMPTZ;
ALTER TABLE accounts ADD COLUMN verified_email TEXT;

CREATE TABLE email_verification_tokens (
    account_id BIGINT PRIMARY KEY REFERENCES accounts (id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    email TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

DELETE FROM password_reset_tokens;

-- +goose Down
LOCK TABLE accounts IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM accounts WHERE email IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot remove email verification while accounts have email addresses';
    END IF;
END;
$$;
-- +goose StatementEnd
DROP TABLE email_verification_tokens;
ALTER TABLE accounts DROP COLUMN email_verified_at;
ALTER TABLE accounts DROP COLUMN verified_email;
