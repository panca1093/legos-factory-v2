-- +goose Up
ALTER TABLE agent_accounts ADD COLUMN api_key_header TEXT NOT NULL DEFAULT 'auth_token';

-- +goose Down
ALTER TABLE agent_accounts DROP COLUMN api_key_header;
