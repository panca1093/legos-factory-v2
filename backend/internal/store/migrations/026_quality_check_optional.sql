-- +goose Up
ALTER TABLE quality_checks ADD COLUMN optional INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE quality_checks DROP COLUMN optional;
