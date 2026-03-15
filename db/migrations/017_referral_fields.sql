-- +goose Up
-- Referral system: every user has a unique code; optional inviter; bonus balance (no logic yet).
ALTER TABLE users ADD COLUMN referral_code TEXT UNIQUE;
ALTER TABLE users ADD COLUMN referred_by TEXT;
ALTER TABLE users ADD COLUMN referral_bonus_balance INTEGER NOT NULL DEFAULT 0;
-- +goose Down
SELECT 1;
