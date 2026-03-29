-- +goose Up
-- Tracks which active legal bundle we last prompted the driver with; when admin bumps versions, fingerprint changes → new oferta.

ALTER TABLE drivers ADD COLUMN legal_terms_prompt_fingerprint TEXT;

-- +goose Down
-- SQLite cannot DROP COLUMN easily in older versions; leave column if rolled back.
