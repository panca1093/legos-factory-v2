-- +goose Up
-- Plan task whose Builder question waits on a human; empty for a planning clarification.
ALTER TABLE work_orders ADD COLUMN clarif_task TEXT NOT NULL DEFAULT '';

-- +goose Down
SELECT 1;
