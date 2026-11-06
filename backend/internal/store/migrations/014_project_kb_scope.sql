-- +goose Up
ALTER TABLE projects ADD COLUMN kb_include TEXT NOT NULL DEFAULT '[]';
ALTER TABLE projects ADD COLUMN kb_exclude TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE projects DROP COLUMN kb_exclude;
ALTER TABLE projects DROP COLUMN kb_include;
