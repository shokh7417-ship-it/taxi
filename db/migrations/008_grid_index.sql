-- +goose Up
-- Simple grid-based index for drivers and ride_requests.

-- drivers: store last known grid cell for quick matching.
ALTER TABLE drivers ADD COLUMN grid_id TEXT;

-- ride_requests: store pickup grid cell.
ALTER TABLE ride_requests ADD COLUMN pickup_grid TEXT;

-- +goose Down
-- SQLite cannot drop columns easily; leave grid_id/pickup_grid in place on downgrade.
SELECT 1;

