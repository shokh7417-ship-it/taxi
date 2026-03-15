-- +goose Up
-- Separate cooldown for "on Online" Live Location message so it is sent when driver taps Online
-- even if the short hint was already shown at /start.
ALTER TABLE drivers ADD COLUMN live_location_on_online_hint_last_sent_at TEXT;

-- +goose Down
SELECT 1;
