-- +goose Up
-- Add first_name, last_name, and plate_number to drivers.
ALTER TABLE drivers ADD COLUMN first_name TEXT;
ALTER TABLE drivers ADD COLUMN last_name TEXT;
ALTER TABLE drivers ADD COLUMN plate_number TEXT;

-- Backfill plate_number from existing plate values.
UPDATE drivers SET plate_number = plate WHERE plate_number IS NULL;

-- +goose Down
SELECT 1;