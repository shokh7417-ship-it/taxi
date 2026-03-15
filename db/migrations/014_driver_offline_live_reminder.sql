-- +goose Up
-- Cooldown for "offline but sharing Live Location" reminder (send once per hour).
ALTER TABLE drivers ADD COLUMN live_location_offline_reminder_last_sent_at TEXT;

-- +goose Down
SELECT 1;
