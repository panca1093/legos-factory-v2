-- +goose Up
ALTER TABLE runs ADD COLUMN cache_read_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN cache_write_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_records ADD COLUMN cache_read_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_records ADD COLUMN cache_write_tokens INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE usage_records DROP COLUMN cache_write_tokens;
ALTER TABLE usage_records DROP COLUMN cache_read_tokens;
ALTER TABLE runs DROP COLUMN cache_write_tokens;
ALTER TABLE runs DROP COLUMN cache_read_tokens;
