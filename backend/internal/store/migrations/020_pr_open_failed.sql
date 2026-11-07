-- +goose Up
-- pr_open_failed_at marks an approved order whose pull request could not be opened, so list views stop counting it as shipped.
ALTER TABLE work_orders ADD COLUMN pr_open_failed_at INTEGER;
UPDATE work_orders SET pr_open_failed_at = (SELECT MAX(created_at) FROM wof_events e WHERE e.wo_id = work_orders.id AND e.kind = 'pr_open_failed')
WHERE state = 'READY' AND pr_url = '' AND pr_ref = '';

-- +goose Down
SELECT 1;
