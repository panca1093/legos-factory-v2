-- +goose Up
UPDATE projects SET git_connection_id = NULL, remote_repo = '';
DELETE FROM connections WHERE scope = 'shared';

-- remote-URL projects: work_orders.project_id has no cascade, so orders go first (their dependents cascade);
-- the contentless kb_fts table is not reached by FK cascades
DELETE FROM jobs WHERE wo_id IN (SELECT id FROM work_orders WHERE project_id IN (SELECT id FROM projects WHERE repo_url NOT LIKE '/%' AND repo_url NOT LIKE '~%'));
DELETE FROM work_orders WHERE project_id IN (SELECT id FROM projects WHERE repo_url NOT LIKE '/%' AND repo_url NOT LIKE '~%');
DELETE FROM kb_fts WHERE rowid IN (SELECT m.fts_rowid FROM kb_fts_map m
    JOIN kb_docs d ON d.id = m.doc_id
    WHERE d.project_id IN (SELECT id FROM projects WHERE repo_url NOT LIKE '/%' AND repo_url NOT LIKE '~%'));
DELETE FROM projects WHERE repo_url NOT LIKE '/%' AND repo_url NOT LIKE '~%';

-- +goose Down
SELECT 1;
