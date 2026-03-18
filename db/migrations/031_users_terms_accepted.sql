-- +goose Up
-- Show Terms of Use only once per user.
ALTER TABLE users ADD COLUMN terms_accepted INTEGER NOT NULL DEFAULT 0;

-- +goose Down
SELECT 1;

