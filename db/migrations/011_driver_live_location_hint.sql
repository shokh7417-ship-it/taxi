-- +goose Up
-- For Live Location UX: track when driver last used live location and when we last showed the hint.
ALTER TABLE drivers ADD COLUMN last_live_location_at TEXT;
ALTER TABLE drivers ADD COLUMN live_location_hint_last_sent_at TEXT;

-- +goose Down
SELECT 1;
