-- +goose Up
-- Drivers must accept the agreement (oferta) before admin approval and dispatch eligibility.
ALTER TABLE drivers ADD COLUMN terms_accepted INTEGER NOT NULL DEFAULT 0;

-- +goose Down
SELECT 1;

