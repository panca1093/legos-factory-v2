-- +goose Up
-- Guard against double-enqueue races: only one active job per
-- (work order, task, kind) may exist at a time.
CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_active_unique
    ON jobs(wo_id, task_id, kind)
    WHERE state IN ('PENDING','CLAIMED','RUNNING');

-- +goose Down
DROP INDEX IF EXISTS idx_jobs_active_unique;
