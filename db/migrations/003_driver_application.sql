-- +goose Up
-- Driver application: phone, car type, color, plate (for rider to see who is coming). No approval flow.
ALTER TABLE drivers ADD COLUMN phone TEXT;
ALTER TABLE drivers ADD COLUMN car_type TEXT;
ALTER TABLE drivers ADD COLUMN color TEXT;
ALTER TABLE drivers ADD COLUMN plate TEXT;
ALTER TABLE drivers ADD COLUMN application_step TEXT;

-- +goose Down
ALTER TABLE drivers DROP COLUMN phone;
ALTER TABLE drivers DROP COLUMN car_type;
ALTER TABLE drivers DROP COLUMN color;
ALTER TABLE drivers DROP COLUMN plate;
ALTER TABLE drivers DROP COLUMN application_step;
