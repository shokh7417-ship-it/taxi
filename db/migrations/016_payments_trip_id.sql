-- +goose Up
-- Link commission payments to trip for total_price in admin API.
ALTER TABLE payments ADD COLUMN trip_id TEXT;
-- +goose Down
SELECT 1;
