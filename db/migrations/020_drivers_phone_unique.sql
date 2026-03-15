-- +goose Up
-- One driver per phone number (prevent fake referral accounts).
CREATE UNIQUE INDEX IF NOT EXISTS idx_drivers_phone_unique ON drivers(phone) WHERE phone IS NOT NULL AND phone != '';
-- +goose Down
DROP INDEX IF EXISTS idx_drivers_phone_unique;
