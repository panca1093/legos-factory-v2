-- +goose Up
CREATE TABLE code_facts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    wo_id      TEXT NOT NULL REFERENCES work_orders(id) ON DELETE CASCADE,
    path       TEXT NOT NULL,
    line       INTEGER NOT NULL DEFAULT 0,
    claim      TEXT NOT NULL,
    by_role    TEXT NOT NULL,
    sha        TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_code_facts_wo ON code_facts(wo_id);

-- +goose Down
DROP TABLE code_facts;
