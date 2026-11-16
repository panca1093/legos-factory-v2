-- +goose Up
CREATE TABLE agent_accounts (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    agent      TEXT NOT NULL,
    label      TEXT NOT NULL,
    home_dir   TEXT NOT NULL,
    active     INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_agent_accounts_user ON agent_accounts(user_id, agent);

CREATE TABLE agent_settings (
    agent   TEXT PRIMARY KEY,
    enabled INTEGER NOT NULL
);

ALTER TABLE users ADD COLUMN claude_usage_opt_in INTEGER NOT NULL DEFAULT 0;
ALTER TABLE work_orders ADD COLUMN role_agents TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE work_orders DROP COLUMN role_agents;
ALTER TABLE users DROP COLUMN claude_usage_opt_in;
DROP TABLE agent_settings;
DROP TABLE agent_accounts;
