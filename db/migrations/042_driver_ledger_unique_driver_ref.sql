-- +goose Up
-- Strict idempotency: one ledger row per (driver_id, reference_type, reference_id). Enables INSERT OR IGNORE for promo/referral.
DROP INDEX IF EXISTS idx_driver_ledger_first3_trip;
DROP INDEX IF EXISTS idx_driver_ledger_referral_reward;

CREATE UNIQUE INDEX IF NOT EXISTS idx_driver_ledger_driver_ref_type_id
ON driver_ledger(driver_id, reference_type, reference_id);

-- +goose Down
DROP INDEX IF EXISTS idx_driver_ledger_driver_ref_type_id;

CREATE UNIQUE INDEX IF NOT EXISTS idx_driver_ledger_first3_trip
ON driver_ledger(driver_id, reference_id)
WHERE reference_type = 'first_3_trip_bonus' AND entry_type = 'PROMO_GRANTED';

CREATE UNIQUE INDEX IF NOT EXISTS idx_driver_ledger_referral_reward
ON driver_ledger(driver_id, reference_id)
WHERE reference_type = 'referral_reward' AND entry_type = 'PROMO_GRANTED';
