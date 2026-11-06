-- +goose Up
ALTER TABLE projects ADD COLUMN contract_tests TEXT NOT NULL DEFAULT 'off';

-- +goose Down
SELECT 1;
