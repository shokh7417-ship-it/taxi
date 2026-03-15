-- +goose Up
-- Admin-controlled fare settings (one active row, id=1). Falls back to env if not set.
CREATE TABLE fare_settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  base_fare INTEGER NOT NULL DEFAULT 4000,
  tier_0_1_km INTEGER NOT NULL DEFAULT 1500,
  tier_1_2_km INTEGER NOT NULL DEFAULT 1200,
  tier_2_plus_km INTEGER NOT NULL DEFAULT 1000,
  updated_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_by INTEGER NULL
);
INSERT OR IGNORE INTO fare_settings (id, base_fare, tier_0_1_km, tier_1_2_km, tier_2_plus_km)
VALUES (1, 4000, 1500, 1200, 1000);

-- +goose Down
DROP TABLE IF EXISTS fare_settings;
