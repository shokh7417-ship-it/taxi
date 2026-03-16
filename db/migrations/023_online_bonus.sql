-- +goose Up
-- Online time bonus: 2000 so'm/hour, max 20000 so'm/day when is_active=1, live_location_active=1, last_live_location_at within 120s.
ALTER TABLE drivers ADD COLUMN online_bonus_so_m_today INTEGER NOT NULL DEFAULT 0;
ALTER TABLE drivers ADD COLUMN online_bonus_last_credited_at TEXT;
ALTER TABLE drivers ADD COLUMN online_bonus_last_day TEXT;
-- +goose Down
SELECT 1;
