-- +goose Up
CREATE TABLE check_proposals (
    project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    status     TEXT NOT NULL,
    proposal   TEXT NOT NULL DEFAULT '',
    error      TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE check_proposals;
