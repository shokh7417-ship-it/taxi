-- +goose Up
-- SQLite/Turso schema (libSQL)
CREATE TABLE users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  telegram_id INTEGER UNIQUE NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('rider','driver')),
  name TEXT,
  phone TEXT,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE drivers (
  user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  is_active INTEGER NOT NULL DEFAULT 0,
  last_lat REAL,
  last_lng REAL,
  last_seen_at TEXT
);

CREATE TABLE ride_requests (
  id TEXT PRIMARY KEY,
  rider_user_id INTEGER NOT NULL REFERENCES users(id),
  pickup_lat REAL NOT NULL,
  pickup_lng REAL NOT NULL,
  radius_km REAL NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('PENDING','ASSIGNED','CANCELLED','EXPIRED')),
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  expires_at TEXT NOT NULL,
  assigned_driver_user_id INTEGER REFERENCES users(id),
  assigned_at TEXT
);

CREATE TABLE request_notifications (
  request_id TEXT NOT NULL REFERENCES ride_requests(id) ON DELETE CASCADE,
  driver_user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  chat_id INTEGER NOT NULL,
  message_id INTEGER NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (request_id, driver_user_id)
);

CREATE TABLE trips (
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

CREATE TABLE trip_locations (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  trip_id TEXT NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
  lat REAL NOT NULL,
  lng REAL NOT NULL,
  ts TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_drivers_active_seen ON drivers(is_active, last_seen_at);
CREATE INDEX idx_ride_requests_status_expires ON ride_requests(status, expires_at);
CREATE INDEX idx_trips_status ON trips(status);
CREATE INDEX idx_trip_locations_trip_ts ON trip_locations(trip_id, ts);

-- +goose Down
DROP TABLE IF EXISTS trip_locations;
DROP TABLE IF EXISTS trips;
DROP TABLE IF EXISTS request_notifications;
DROP TABLE IF EXISTS ride_requests;
DROP TABLE IF EXISTS drivers;
DROP TABLE IF EXISTS users;
