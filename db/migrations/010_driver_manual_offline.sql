-- +goose Up
-- When driver presses "Offline", set manual_offline=1 so location updates do not auto-reactivate.
ALTER TABLE drivers ADD COLUMN manual_offline INTEGER NOT NULL DEFAULT 0;

-- +goose Down
SELECT 1;
