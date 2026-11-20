-- +goose Up
ALTER TABLE usage_records ADD COLUMN agent TEXT NOT NULL DEFAULT '';
ALTER TABLE usage_records ADD COLUMN account_id TEXT NOT NULL DEFAULT '';
ALTER TABLE usage_records ADD COLUMN served_model TEXT NOT NULL DEFAULT '';
ALTER TABLE usage_records ADD COLUMN provider_host TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE usage_records DROP COLUMN provider_host;
ALTER TABLE usage_records DROP COLUMN served_model;
ALTER TABLE usage_records DROP COLUMN account_id;
ALTER TABLE usage_records DROP COLUMN agent;
