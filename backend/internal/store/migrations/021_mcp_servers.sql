-- +goose Up
-- name becomes the mcp__<name>__ prefix of the server's tools, so the database enforces its shape.
CREATE TABLE mcp_servers (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 40 AND name NOT GLOB '*[^a-z0-9_]*'),
  transport     TEXT NOT NULL CHECK (transport IN ('stdio','http')),
  command       TEXT NOT NULL DEFAULT '',
  args          TEXT NOT NULL DEFAULT '[]',
  url           TEXT NOT NULL DEFAULT '',
  auth          TEXT NOT NULL DEFAULT 'none' CHECK (auth IN ('none','token')),
  secret_enc    BLOB NOT NULL DEFAULT x'',
  roles         TEXT NOT NULL DEFAULT '[]',
  tools         TEXT NOT NULL DEFAULT '[]',
  status        TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  last_error    TEXT NOT NULL DEFAULT '',
  discovered_at INTEGER,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);

-- +goose Down
DROP TABLE mcp_servers;
