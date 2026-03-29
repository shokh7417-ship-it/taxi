-- +goose Up
-- Prevents duplicate Telegram admin packets when a pending driver re-accepts oferta after a terms bump.

ALTER TABLE drivers ADD COLUMN application_admin_sent INTEGER NOT NULL DEFAULT 0;

-- +goose Down
-- SQLite: leave column on rollback.
