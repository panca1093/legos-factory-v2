-- +goose Up
ALTER TABLE work_orders ADD COLUMN base_branch TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE work_orders DROP COLUMN base_branch;
