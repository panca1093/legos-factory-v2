-- +goose Up
-- Governance snapshot: Foreman/Architect models resolved at creation.
ALTER TABLE work_orders ADD COLUMN foreman_model TEXT NOT NULL DEFAULT '';
ALTER TABLE work_orders ADD COLUMN architect_model TEXT NOT NULL DEFAULT '';
-- Foreman plan review: '' (none yet) | pending | approved
ALTER TABLE work_orders ADD COLUMN plan_review TEXT NOT NULL DEFAULT '';
ALTER TABLE work_orders ADD COLUMN review_rounds INTEGER NOT NULL DEFAULT 0;
-- Pause: the scheduler does not claim jobs of a paused work order.
ALTER TABLE work_orders ADD COLUMN paused_at INTEGER;
ALTER TABLE work_orders ADD COLUMN pause_reason TEXT NOT NULL DEFAULT '';

-- Specialist inspections and human dispositions.
ALTER TABLE findings ADD COLUMN specialty TEXT NOT NULL DEFAULT '';
ALTER TABLE findings ADD COLUMN note TEXT NOT NULL DEFAULT '';
ALTER TABLE findings ADD COLUMN decided_by TEXT NOT NULL DEFAULT '';
ALTER TABLE findings ADD COLUMN updated_at INTEGER;

-- +goose Down
SELECT 1;
