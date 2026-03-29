-- +goose Up
-- Promo vs cash wallets + append-only driver_ledger for platform accounting (not payment settlement).

CREATE TABLE driver_ledger (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  driver_id INTEGER NOT NULL REFERENCES drivers(user_id) ON DELETE CASCADE,
  bucket TEXT NOT NULL CHECK (bucket IN ('promo','cash')),
  entry_type TEXT NOT NULL,
  amount INTEGER NOT NULL,
  reference_type TEXT,
  reference_id TEXT,
  note TEXT,
  metadata_json TEXT,
  expires_at TEXT,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_driver_ledger_driver_created ON driver_ledger(driver_id, created_at DESC);

ALTER TABLE drivers ADD COLUMN promo_balance INTEGER NOT NULL DEFAULT 0;
ALTER TABLE drivers ADD COLUMN cash_balance INTEGER NOT NULL DEFAULT 0;

-- Historical single balance was promotional platform credit only (not withdrawable cash).
UPDATE drivers SET promo_balance = COALESCE(balance, 0), cash_balance = 0;
UPDATE drivers SET balance = promo_balance + cash_balance;

-- +goose Down
DROP INDEX IF EXISTS idx_driver_ledger_driver_created;
DROP TABLE IF EXISTS driver_ledger;
-- promo_balance / cash_balance columns remain; balance already equals their sum from Up.
