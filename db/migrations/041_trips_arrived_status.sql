-- +goose Up
-- Add ARRIVED (driver at pickup before STARTED) and arrived_at. SQLite: extend status CHECK via table recreate.
CREATE TABLE trips_new (
  id TEXT PRIMARY KEY,
  request_id TEXT UNIQUE NOT NULL REFERENCES ride_requests(id) ON DELETE CASCADE,
  driver_user_id INTEGER NOT NULL REFERENCES users(id),
  rider_user_id INTEGER NOT NULL REFERENCES users(id),
  status TEXT NOT NULL CHECK (status IN ('WAITING','ARRIVED','STARTED','FINISHED','CANCELLED','CANCELLED_BY_DRIVER','CANCELLED_BY_RIDER')),
  started_at TEXT,
  finished_at TEXT,
  distance_m INTEGER NOT NULL DEFAULT 0,
  fare_amount INTEGER NOT NULL DEFAULT 0,
  cancelled_at TEXT NULL,
  cancelled_by VARCHAR(20) NULL,
  cancel_reason TEXT NULL,
  rider_bonus_used INTEGER NOT NULL DEFAULT 0,
  arrived_at TEXT NULL
);
INSERT INTO trips_new (
  id, request_id, driver_user_id, rider_user_id, status, started_at, finished_at, distance_m, fare_amount,
  cancelled_at, cancelled_by, cancel_reason, rider_bonus_used, arrived_at
)
SELECT
  id, request_id, driver_user_id, rider_user_id, status, started_at, finished_at, distance_m, fare_amount,
  cancelled_at, cancelled_by, cancel_reason, rider_bonus_used, NULL
FROM trips;
DROP TABLE trips;
ALTER TABLE trips_new RENAME TO trips;
CREATE INDEX idx_trips_status ON trips(status);

-- +goose Down
CREATE TABLE trips_old (
  id TEXT PRIMARY KEY,
  request_id TEXT UNIQUE NOT NULL REFERENCES ride_requests(id) ON DELETE CASCADE,
  driver_user_id INTEGER NOT NULL REFERENCES users(id),
  rider_user_id INTEGER NOT NULL REFERENCES users(id),
  status TEXT NOT NULL CHECK (status IN ('WAITING','STARTED','FINISHED','CANCELLED','CANCELLED_BY_DRIVER','CANCELLED_BY_RIDER')),
  started_at TEXT,
  finished_at TEXT,
  distance_m INTEGER NOT NULL DEFAULT 0,
  fare_amount INTEGER NOT NULL DEFAULT 0,
  cancelled_at TEXT NULL,
  cancelled_by VARCHAR(20) NULL,
  cancel_reason TEXT NULL,
  rider_bonus_used INTEGER NOT NULL DEFAULT 0
);
INSERT INTO trips_old (
  id, request_id, driver_user_id, rider_user_id, status, started_at, finished_at, distance_m, fare_amount,
  cancelled_at, cancelled_by, cancel_reason, rider_bonus_used
)
SELECT
  id, request_id, driver_user_id, rider_user_id,
  CASE WHEN status = 'ARRIVED' THEN 'WAITING' ELSE status END,
  started_at, finished_at, distance_m, fare_amount,
  cancelled_at, cancelled_by, cancel_reason, rider_bonus_used
FROM trips;
DROP TABLE trips;
ALTER TABLE trips_old RENAME TO trips;
CREATE INDEX idx_trips_status ON trips(status);
