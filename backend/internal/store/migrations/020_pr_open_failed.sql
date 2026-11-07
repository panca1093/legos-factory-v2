-- +goose Up
-- pr_open_failed_at marks an approved order whose pull request could not be opened, so list views stop counting it as shipped.
ALTER TABLE work_orders ADD COLUMN pr_open_failed_at INTEGER;

-- +goose Down
SELECT 1;
