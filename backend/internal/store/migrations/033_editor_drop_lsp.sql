-- +goose Up
DROP TABLE editor_server_paths;
ALTER TABLE editor_folders DROP COLUMN trusted;

-- +goose Down
ALTER TABLE editor_folders ADD COLUMN trusted INTEGER NOT NULL DEFAULT 0;
CREATE TABLE editor_server_paths (             -- confirmed custom language server binaries
    user_id TEXT NOT NULL, server TEXT NOT NULL, path TEXT NOT NULL, confirmed_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, server)
);
