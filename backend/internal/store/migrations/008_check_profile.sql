-- +goose Up
ALTER TABLE work_orders ADD COLUMN check_profile TEXT NOT NULL DEFAULT '';
ALTER TABLE work_orders ADD COLUMN verify_failures INTEGER NOT NULL DEFAULT 0;

-- +goose Down
SELECT 1;
