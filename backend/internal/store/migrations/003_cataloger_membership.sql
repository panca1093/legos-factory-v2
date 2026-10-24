-- +goose Up
-- Single-workspace MVP: every project belongs to the default workspace, so
-- knowledge records can already carry the workspace boundary.
CREATE TABLE workspaces (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    created_at  INTEGER NOT NULL
);
INSERT INTO workspaces (id, name, created_at) VALUES ('ws_default', 'Default workspace', strftime('%s','now'));

ALTER TABLE projects ADD COLUMN workspace_id TEXT NOT NULL DEFAULT 'ws_default' REFERENCES workspaces(id);

CREATE TABLE project_members (
    project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (project_id, user_id)
);
CREATE INDEX idx_members_user ON project_members(user_id);

-- preserve pre-membership access: existing users keep every existing project
INSERT INTO project_members (project_id, user_id, created_at)
    SELECT p.id, u.id, strftime('%s','now') FROM projects p CROSS JOIN users u;

ALTER TABLE work_orders ADD COLUMN commit_sha TEXT NOT NULL DEFAULT '';
ALTER TABLE quality_checks ADD COLUMN commit_sha TEXT NOT NULL DEFAULT '';

CREATE TABLE knowledge_records (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL REFERENCES workspaces(id),
    project_id    TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    wo_id         TEXT REFERENCES work_orders(id) ON DELETE SET NULL,
    repo_url      TEXT NOT NULL DEFAULT '',
    commit_sha    TEXT NOT NULL DEFAULT '',
    kind          TEXT NOT NULL,  -- plan_approved|clarification|verification|findings|implementation|readiness|pr_decision|work_order_completed|summary
    trust         TEXT NOT NULL,  -- verified|human_decision|ai_interpretation
    title         TEXT NOT NULL,
    body          TEXT NOT NULL DEFAULT '',
    actor         TEXT NOT NULL,
    source_refs   TEXT NOT NULL DEFAULT '[]', -- JSON: ["event:12","knowledge:kn_x","run:run_y"]
    created_at    INTEGER NOT NULL
);
CREATE INDEX idx_knowledge_project ON knowledge_records(project_id, created_at);
CREATE INDEX idx_knowledge_wo ON knowledge_records(wo_id);

CREATE VIRTUAL TABLE knowledge_fts USING fts5(
    title, body, content='knowledge_records', content_rowid='rowid'
);

-- +goose StatementBegin
CREATE TRIGGER knowledge_ai AFTER INSERT ON knowledge_records BEGIN
    INSERT INTO knowledge_fts(rowid, title, body) VALUES (new.rowid, new.title, new.body);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER knowledge_ad AFTER DELETE ON knowledge_records BEGIN
    INSERT INTO knowledge_fts(knowledge_fts, rowid, title, body) VALUES ('delete', old.rowid, old.title, old.body);
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS knowledge_ad;
DROP TRIGGER IF EXISTS knowledge_ai;
DROP TABLE IF EXISTS knowledge_fts;
DROP TABLE IF EXISTS knowledge_records;
DROP TABLE IF EXISTS project_members;
