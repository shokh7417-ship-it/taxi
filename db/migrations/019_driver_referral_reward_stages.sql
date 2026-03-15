-- +goose Up
-- Driver referral: stage1 paid when new driver registers; stage2 paid when referred driver completes 5 trips.
ALTER TABLE users ADD COLUMN referral_stage1_reward_paid INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN referral_stage2_reward_paid INTEGER NOT NULL DEFAULT 0;
-- +goose Down
SELECT 1;
