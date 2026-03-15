-- +goose Up
-- Admin-controlled commission percentage (taken from trip price when InfiniteDriverBalance is false).
ALTER TABLE fare_settings ADD COLUMN commission_percent INTEGER NOT NULL DEFAULT 5;

-- +goose Down
-- SQLite does not support DROP COLUMN; no-op.
SELECT 1;
