package store

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestMigration029DropsSharedConnectionsAndRemoteProjects(t *testing.T) {
	dsn := "file:" + url.PathEscape(filepath.Join(t.TempDir(), "old.db")) + "?_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, "migrations", 28); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users (id, email, name, role, created_at) VALUES ('u1','u1@t','u1','ADMIN',1)`,
		`INSERT INTO connections (id, workspace_id, provider, name, scope, owner_id, base_url, secret_enc, created_at, updated_at)
			VALUES ('cs','ws_default','github','org','shared','u1','https://api.github.com',x'01',1,1),
			       ('cp','ws_default','jira','mine','personal','u1','https://x.atlassian.net',x'02',1,1)`,
		`INSERT INTO projects (id, name, repo_url, workspace_id, created_at, git_connection_id, remote_repo) VALUES
			('local','Local','/work/app','ws_default',1,'cp',''),
			('tilde','Tilde','~/app','ws_default',1,NULL,''),
			('remote','Remote','https://github.com/acme/app.git','ws_default',1,'cs','acme/app'),
			('empty','Empty','','ws_default',1,NULL,'')`,
		`INSERT INTO work_orders (id, project_id, title, objective, state, created_by, created_at, updated_at)
			VALUES ('wl','local','t','o','CREATED','u1',1,1), ('wr','remote','t','o','CREATED','u1',1,1)`,
		`INSERT INTO plan_tasks (id, work_order_id, seq, title, created_at) VALUES ('tr','wr',0,'t',1)`,
		`INSERT INTO kb_docs (id, source, project_id, path, commit_sha, content_hash, title, indexed_at)
			VALUES ('dr','repo','remote','a.md','s','h','A',1), ('dl','repo','local','a.md','s','h','A',1)`,
		`INSERT INTO kb_fts_map (doc_id, fts_rowid) VALUES ('dr', 1), ('dl', 2)`,
		`INSERT INTO kb_fts (rowid, title, description, headings, body) VALUES (1,'a','','','x'), (2,'a','','','y')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v: %s", err, q)
		}
	}

	if err := goose.UpTo(db, "migrations", 29); err != nil {
		t.Fatal(err)
	}

	ids := func(q string) string {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := ""
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			out += id + ","
		}
		return out
	}
	if got := ids(`SELECT id FROM connections ORDER BY id`); got != "cp," {
		t.Fatalf("connections: %q", got)
	}
	if got := ids(`SELECT id FROM projects ORDER BY id`); got != "local,tilde," {
		t.Fatalf("projects: %q", got)
	}
	if got := ids(`SELECT id FROM work_orders`); got != "wl," {
		t.Fatalf("work orders: %q", got)
	}
	if got := ids(`SELECT id FROM plan_tasks`); got != "" {
		t.Fatalf("plan tasks of a removed project must cascade: %q", got)
	}
	if got := ids(`SELECT doc_id FROM kb_fts_map`); got != "dl," {
		t.Fatalf("kb map: %q", got)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM projects WHERE git_connection_id IS NOT NULL OR remote_repo != ''`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("bindings must be cleared: %d %v", n, err)
	}
}
