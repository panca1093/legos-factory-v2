-- +goose Up
-- Foreman intake brief, Architect spec, and the Foreman's answers to Architect questions.
ALTER TABLE work_orders ADD COLUMN brief TEXT NOT NULL DEFAULT '';
ALTER TABLE work_orders ADD COLUMN spec TEXT NOT NULL DEFAULT '';
ALTER TABLE work_orders ADD COLUMN clarif_rounds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE work_orders ADD COLUMN clarif_log TEXT NOT NULL DEFAULT '';
-- JSON array of the files or areas a task may touch.
ALTER TABLE plan_tasks ADD COLUMN scope TEXT NOT NULL DEFAULT '';

-- +goose Down
SELECT 1;
