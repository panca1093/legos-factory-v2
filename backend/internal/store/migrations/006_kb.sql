-- +goose Up
CREATE TABLE kb_docs (
    id            TEXT PRIMARY KEY,
    source        TEXT NOT NULL,
    project_id    TEXT REFERENCES projects(id) ON DELETE CASCADE,
    path          TEXT NOT NULL,
    commit_sha    TEXT NOT NULL,
    content_hash  TEXT NOT NULL,
    type          TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'stable',
    trust         TEXT NOT NULL DEFAULT 'unverified',
    title         TEXT NOT NULL,
    description   TEXT NOT NULL DEFAULT '',
    meta          TEXT NOT NULL DEFAULT '{}',
    indexed_at    INTEGER NOT NULL
);
CREATE INDEX idx_kb_docs_scope ON kb_docs(source, project_id);

CREATE TABLE kb_links (
    from_doc  TEXT NOT NULL REFERENCES kb_docs(id) ON DELETE CASCADE,
    to_doc    TEXT NOT NULL,
    PRIMARY KEY (from_doc, to_doc)
);
CREATE INDEX idx_kb_links_to ON kb_links(to_doc);

CREATE VIRTUAL TABLE kb_fts USING fts5(title, description, headings, body, content='', contentless_delete=1);

CREATE TABLE kb_fts_map (
    doc_id     TEXT PRIMARY KEY REFERENCES kb_docs(id) ON DELETE CASCADE,
    fts_rowid  INTEGER NOT NULL UNIQUE
);

CREATE TABLE kb_vectors (
    doc_id    TEXT PRIMARY KEY REFERENCES kb_docs(id) ON DELETE CASCADE,
    embedder  TEXT NOT NULL,
    vec       BLOB NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS kb_vectors;
DROP TABLE IF EXISTS kb_fts_map;
DROP TABLE IF EXISTS kb_fts;
DROP TABLE IF EXISTS kb_links;
DROP TABLE IF EXISTS kb_docs;
