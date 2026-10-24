-- +goose Up
-- Provider connections. secret_enc holds the AES-GCM sealed credential and is
-- never returned by the API.
CREATE TABLE connections (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL REFERENCES workspaces(id),
    provider      TEXT NOT NULL,  -- github|gitlab|bitbucket|jira
    name          TEXT NOT NULL,
    scope         TEXT NOT NULL,  -- shared|personal
    owner_id      TEXT NOT NULL REFERENCES users(id),
    base_url      TEXT NOT NULL,
    username      TEXT NOT NULL DEFAULT '',
    secret_enc    BLOB NOT NULL,
    status        TEXT NOT NULL DEFAULT 'active', -- active|disabled
    account       TEXT NOT NULL DEFAULT '',
    verified_at   INTEGER,
    last_error    TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);
CREATE INDEX idx_connections_owner ON connections(owner_id);

ALTER TABLE projects ADD COLUMN git_connection_id TEXT REFERENCES connections(id) ON DELETE SET NULL;
ALTER TABLE projects ADD COLUMN remote_repo TEXT NOT NULL DEFAULT '';

ALTER TABLE work_orders ADD COLUMN source_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE work_orders ADD COLUMN pr_url TEXT NOT NULL DEFAULT '';
ALTER TABLE work_orders ADD COLUMN pr_ref TEXT NOT NULL DEFAULT '';

-- +goose Down
DROP TABLE IF EXISTS connections;
