-- +goose Up
CREATE TABLE editor_folders (
    id TEXT PRIMARY KEY, user_id TEXT NOT NULL,
    path TEXT NOT NULL,                       -- absolute, cleaned, symlinks resolved
    kind TEXT NOT NULL,                       -- project | worktree | folder
    project_id TEXT NOT NULL DEFAULT '', wo_id TEXT NOT NULL DEFAULT '', task_id TEXT NOT NULL DEFAULT '',
    trusted INTEGER NOT NULL DEFAULT 0,
    opened_at INTEGER NOT NULL,
    UNIQUE (user_id, path)
);
CREATE TABLE editor_project_folders (          -- a user's chosen local folder for a project (no UI since #91)
    user_id TEXT NOT NULL, project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    path TEXT NOT NULL, PRIMARY KEY (user_id, project_id)
);
CREATE TABLE editor_server_paths (             -- confirmed custom language server binaries
    user_id TEXT NOT NULL, server TEXT NOT NULL, path TEXT NOT NULL, confirmed_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, server)
);

-- +goose Down
DROP TABLE editor_server_paths;
DROP TABLE editor_project_folders;
DROP TABLE editor_folders;
