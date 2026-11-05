-- +goose Up
-- pr_merged_at / pr_closed_at (unix seconds) are set when the poller sees the PR merged or closed;
-- pr_track_failed_at stops polling an order whose PR can never be read.
ALTER TABLE work_orders ADD COLUMN pr_merged_at INTEGER;
ALTER TABLE work_orders ADD COLUMN pr_closed_at INTEGER;
ALTER TABLE work_orders ADD COLUMN pr_track_failed_at INTEGER;
CREATE INDEX idx_usage_user_created ON usage_records(user_id, created_at);

-- +goose Down
SELECT 1;
