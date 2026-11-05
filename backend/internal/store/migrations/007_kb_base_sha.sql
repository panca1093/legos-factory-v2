-- +goose Up
ALTER TABLE work_orders ADD COLUMN kb_base_sha TEXT NOT NULL DEFAULT '';

-- +goose Down
SELECT 1;
