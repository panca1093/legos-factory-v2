-- +goose Up
ALTER TABLE agent_accounts ADD COLUMN api_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_accounts ADD COLUMN api_base_url TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_accounts ADD COLUMN api_key_sealed BLOB;
ALTER TABLE agent_accounts ADD COLUMN api_models TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE agent_accounts DROP COLUMN api_models;
ALTER TABLE agent_accounts DROP COLUMN api_key_sealed;
ALTER TABLE agent_accounts DROP COLUMN api_base_url;
ALTER TABLE agent_accounts DROP COLUMN api_provider;
