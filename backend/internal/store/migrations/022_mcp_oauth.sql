-- +goose Up
CREATE TABLE mcp_servers_new (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 40 AND name NOT GLOB '*[^a-z0-9_]*' AND name NOT GLOB '*__*' AND name NOT GLOB '_*' AND name NOT GLOB '*_'),
  transport     TEXT NOT NULL CHECK (transport IN ('stdio','http')),
  command       TEXT NOT NULL DEFAULT '',
  args          TEXT NOT NULL DEFAULT '[]',
  url           TEXT NOT NULL DEFAULT '',
  auth          TEXT NOT NULL DEFAULT 'none' CHECK (auth IN ('none','token','oauth')),
  secret_enc    BLOB NOT NULL DEFAULT x'',
  roles         TEXT NOT NULL DEFAULT '[]',
  tools         TEXT NOT NULL DEFAULT '[]',
  status        TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled','needs_sign_in')),
  last_error    TEXT NOT NULL DEFAULT '',
  discovered_at INTEGER,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);
INSERT INTO mcp_servers_new SELECT * FROM mcp_servers;
DROP TABLE mcp_servers;
ALTER TABLE mcp_servers_new RENAME TO mcp_servers;

-- +goose Down
SELECT 1;
