-- +goose Up
DELETE FROM kb_fts WHERE rowid IN (SELECT m.fts_rowid FROM kb_fts_map m JOIN kb_docs d ON d.id = m.doc_id WHERE d.source = 'repo');
DELETE FROM kb_fts_map WHERE doc_id IN (SELECT id FROM kb_docs WHERE source = 'repo');
DELETE FROM kb_vectors WHERE doc_id IN (SELECT id FROM kb_docs WHERE source = 'repo');
DELETE FROM kb_links WHERE from_doc IN (SELECT id FROM kb_docs WHERE source = 'repo') OR to_doc IN (SELECT id FROM kb_docs WHERE source = 'repo');
DELETE FROM kb_docs WHERE source = 'repo';
ALTER TABLE projects DROP COLUMN kb_exclude;
ALTER TABLE projects DROP COLUMN kb_include;

-- +goose Down
ALTER TABLE projects ADD COLUMN kb_include TEXT NOT NULL DEFAULT '[]';
ALTER TABLE projects ADD COLUMN kb_exclude TEXT NOT NULL DEFAULT '[]';
