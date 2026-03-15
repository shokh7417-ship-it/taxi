-- +goose Up
-- Driver balances and payments ledger

-- drivers: add balance and total_paid (in smallest currency units, e.g. so'm)
ALTER TABLE drivers ADD COLUMN balance INTEGER NOT NULL DEFAULT 0;
ALTER TABLE drivers ADD COLUMN total_paid INTEGER NOT NULL DEFAULT 0;

-- payments table: tracks deposits, commissions, and manual adjustments
CREATE TABLE payments (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  driver_id INTEGER NOT NULL REFERENCES drivers(user_id) ON DELETE CASCADE,
  amount INTEGER NOT NULL,
  type TEXT NOT NULL CHECK (type IN ('deposit','commission','adjustment')),
  note TEXT,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_payments_driver_created_at ON payments(driver_id, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_payments_driver_created_at;
DROP TABLE IF EXISTS payments;
-- SQLite cannot drop columns easily; leave balance/total_paid in place on downgrade.
SELECT 1;

