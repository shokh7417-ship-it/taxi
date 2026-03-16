-- +goose Up
-- One-time welcome bonus message after driver registration (UX only).
ALTER TABLE drivers ADD COLUMN welcome_bonus_message_sent INTEGER NOT NULL DEFAULT 0;
-- +goose Down
SELECT 1;
