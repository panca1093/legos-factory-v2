-- +goose Up
ALTER TABLE projects DROP COLUMN contract_tests;

-- +goose Down
ALTER TABLE projects ADD COLUMN contract_tests TEXT NOT NULL DEFAULT 'off';
