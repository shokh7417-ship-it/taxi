-- +goose Up
-- Language preference for users (Latin or Cyrillic alphabet).
ALTER TABLE users ADD COLUMN lang TEXT;

-- +goose Down
SELECT 1;
