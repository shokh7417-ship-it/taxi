-- +goose Up
-- Dispatch eligibility: only drivers with active Telegram Live Location (live_location_active=1 and last_live_location_at within 90s).
ALTER TABLE drivers ADD COLUMN live_location_active INTEGER NOT NULL DEFAULT 0;
-- +goose Down
SELECT 1;
