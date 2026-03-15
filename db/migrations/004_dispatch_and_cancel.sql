-- +goose Up
-- Smart dispatch: request_notifications with id and status (SENT/ACCEPTED/REJECTED/TIMEOUT)
-- Trip cancellation: add CANCELLED_BY_DRIVER, CANCELLED_BY_RIDER to trips

-- Recreate request_notifications with id and status
CREATE TABLE request_notifications_new (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id TEXT NOT NULL REFERENCES ride_requests(id) ON DELETE CASCADE,
  driver_user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  chat_id INTEGER NOT NULL,
  message_id INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'SENT' CHECK (status IN ('SENT','ACCEPTED','REJECTED','TIMEOUT')),
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO request_notifications_new (request_id, driver_user_id, chat_id, message_id, status, created_at)
SELECT request_id, driver_user_id, chat_id, message_id, 'SENT', created_at FROM request_notifications;
DROP TABLE request_notifications;
ALTER TABLE request_notifications_new RENAME TO request_notifications;
CREATE UNIQUE INDEX idx_request_notifications_request_driver ON request_notifications(request_id, driver_user_id);
CREATE INDEX idx_request_notifications_status ON request_notifications(request_id, status);

-- Trips: add CANCELLED_BY_DRIVER, CANCELLED_BY_RIDER (SQLite: recreate table to change CHECK)
CREATE TABLE trips_new (
  id TEXT PRIMARY KEY,
  request_id TEXT UNIQUE NOT NULL REFERENCES ride_requests(id) ON DELETE CASCADE,
  driver_user_id INTEGER NOT NULL REFERENCES users(id),
  rider_user_id INTEGER NOT NULL REFERENCES users(id),
  status TEXT NOT NULL CHECK (status IN ('WAITING','STARTED','FINISHED','CANCELLED','CANCELLED_BY_DRIVER','CANCELLED_BY_RIDER')),
  started_at TEXT,
  finished_at TEXT,
  distance_m INTEGER NOT NULL DEFAULT 0,
  fare_amount INTEGER NOT NULL DEFAULT 0
);
INSERT INTO trips_new (id, request_id, driver_user_id, rider_user_id, status, started_at, finished_at, distance_m, fare_amount)
SELECT id, request_id, driver_user_id, rider_user_id, status, started_at, finished_at, distance_m, fare_amount FROM trips;
DROP TABLE trips;
ALTER TABLE trips_new RENAME TO trips;
CREATE INDEX idx_trips_status ON trips(status);

-- +goose Down
-- Revert trips to original CHECK
CREATE TABLE trips_old (
  id TEXT PRIMARY KEY,
  request_id TEXT UNIQUE NOT NULL REFERENCES ride_requests(id) ON DELETE CASCADE,
  driver_user_id INTEGER NOT NULL REFERENCES users(id),
  rider_user_id INTEGER NOT NULL REFERENCES users(id),
  status TEXT NOT NULL CHECK (status IN ('WAITING','STARTED','FINISHED','CANCELLED')),
  started_at TEXT,
  finished_at TEXT,
  distance_m INTEGER NOT NULL DEFAULT 0,
  fare_amount INTEGER NOT NULL DEFAULT 0
);
INSERT INTO trips_old SELECT id, request_id, driver_user_id, rider_user_id, status, started_at, finished_at, distance_m, fare_amount FROM trips WHERE status IN ('WAITING','STARTED','FINISHED','CANCELLED');
DROP TABLE trips;
ALTER TABLE trips_old RENAME TO trips;
CREATE INDEX idx_trips_status ON trips(status);

-- Revert request_notifications (drop id and status)
CREATE TABLE request_notifications_old (
  request_id TEXT NOT NULL REFERENCES ride_requests(id) ON DELETE CASCADE,
  driver_user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  chat_id INTEGER NOT NULL,
  message_id INTEGER NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (request_id, driver_user_id)
);
INSERT INTO request_notifications_old (request_id, driver_user_id, chat_id, message_id, created_at)
SELECT request_id, driver_user_id, chat_id, message_id, created_at FROM request_notifications;
DROP TABLE request_notifications;
ALTER TABLE request_notifications_old RENAME TO request_notifications;
