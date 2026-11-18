-- +goose Up
CREATE TABLE agent_role_accounts (
    user_id    TEXT NOT NULL,
    agent      TEXT NOT NULL,
    role       TEXT NOT NULL,
    account_id TEXT NOT NULL,
    PRIMARY KEY (user_id, agent, role)
);

-- +goose Down
DROP TABLE agent_role_accounts;
