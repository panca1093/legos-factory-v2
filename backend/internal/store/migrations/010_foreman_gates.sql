-- +goose Up
-- plan_revisions counts plan-gate revisions since the last human decision;
-- architect_report is the Architect's latest verification (JSON: revision, verdicts, deviations).
ALTER TABLE work_orders ADD COLUMN plan_revisions INTEGER NOT NULL DEFAULT 0;
ALTER TABLE work_orders ADD COLUMN architect_report TEXT NOT NULL DEFAULT '';

-- +goose Down
SELECT 1;
