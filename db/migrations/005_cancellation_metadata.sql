-- +goose Up
-- Cancellation metadata for trips
ALTER TABLE trips ADD COLUMN cancelled_at TEXT NULL;
ALTER TABLE trips ADD COLUMN cancelled_by VARCHAR(20) NULL;
ALTER TABLE trips ADD COLUMN cancel_reason TEXT NULL;

-- +goose Down
-- SQLite does not support DROP COLUMN; would require table recreate to remove columns.
-- For rollback, leave columns in place or add a separate migration to recreate table without them.
SELECT 1;
