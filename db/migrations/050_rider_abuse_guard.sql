-- Minimal tables for rider cancel abuse tracking and temporary blocks.

CREATE TABLE IF NOT EXISTS rider_abuse_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    rider_user_id INTEGER NOT NULL,
    trip_id TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_rider_abuse_events_rider_created_at
    ON rider_abuse_events (rider_user_id, created_at);

CREATE TABLE IF NOT EXISTS rider_abuse_state (
    rider_user_id INTEGER PRIMARY KEY,
    block_until TEXT,
    last_warning_at TEXT,
    escalation_level INTEGER NOT NULL DEFAULT 0
);

