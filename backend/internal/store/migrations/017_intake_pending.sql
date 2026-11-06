-- +goose Up
-- True while the Foreman brief still waits on a human answer to an intake question.
ALTER TABLE work_orders ADD COLUMN intake_pending INTEGER NOT NULL DEFAULT 0;

-- +goose Down
SELECT 1;
