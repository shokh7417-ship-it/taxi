-- +goose Up
-- How much rider referral bonus was applied to this trip (fare discount).
ALTER TABLE trips ADD COLUMN rider_bonus_used INTEGER NOT NULL DEFAULT 0;
-- +goose Down
SELECT 1;
