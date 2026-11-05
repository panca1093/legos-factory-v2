-- +goose Up
-- pr_merged_at / pr_closed_at (unix seconds) are set when the poller sees the PR merged or closed unmerged.
ALTER TABLE work_orders ADD COLUMN pr_merged_at INTEGER;
ALTER TABLE work_orders ADD COLUMN pr_closed_at INTEGER;

-- +goose Down
SELECT 1;
