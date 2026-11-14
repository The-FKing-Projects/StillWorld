-- +goose Up
ALTER TABLE accounts ADD COLUMN disabled_at TIMESTAMPTZ;

-- +goose Down
LOCK TABLE accounts IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM accounts WHERE disabled_at IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot remove account disabling while accounts are disabled';
    END IF;
END;
$$;
-- +goose StatementEnd
ALTER TABLE accounts DROP COLUMN disabled_at;
