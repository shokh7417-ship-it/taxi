-- +goose Up
-- Cooldown for static location rejection message (avoid spam when driver sends static location repeatedly).
ALTER TABLE drivers ADD COLUMN static_location_rejection_last_sent_at TEXT;

-- +goose Down
SELECT 1;
