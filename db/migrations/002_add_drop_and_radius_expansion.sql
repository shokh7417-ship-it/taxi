-- +goose Up
-- SQLite: add drop coords and radius_expanded_at to ride_requests
ALTER TABLE ride_requests ADD COLUMN drop_lat REAL;
ALTER TABLE ride_requests ADD COLUMN drop_lng REAL;
ALTER TABLE ride_requests ADD COLUMN radius_expanded_at TEXT;

-- +goose Down
-- libSQL/SQLite 3.35+ supports DROP COLUMN
ALTER TABLE ride_requests DROP COLUMN drop_lat;
ALTER TABLE ride_requests DROP COLUMN drop_lng;
ALTER TABLE ride_requests DROP COLUMN radius_expanded_at;
