-- +goose Up
-- Document verification: license and vehicle doc file_ids; verification_status (pending | approved | rejected).
ALTER TABLE drivers ADD COLUMN license_photo_file_id TEXT;
ALTER TABLE drivers ADD COLUMN vehicle_doc_file_id TEXT;
ALTER TABLE drivers ADD COLUMN verification_status TEXT;
-- Existing drivers remain eligible for orders.
UPDATE drivers SET verification_status = 'approved' WHERE verification_status IS NULL;
-- +goose Down
SELECT 1;
