package store

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"legos-factory/backend/internal/model"
)

func TestMCPRepoRoundTripKeepsSealedSecret(t *testing.T) {
	db := openTestDB(t)
	r := &MCPRepo{DB: db}
	ctx := context.Background()
	box, _ := NewSecretBox(bytes.Repeat([]byte{3}, 32))
	sealed, _ := box.Seal([]byte(`{"TOKEN":"s3cret"}`))
	s := &model.MCPServerRec{Name: "jira_docs", Transport: model.MCPStdio, Command: "/bin/x", Args: []string{"-a"}, Auth: model.MCPAuthToken,
		Roles: []string{"builder"}, Tools: []model.MCPTool{{Name: "t", ReadOnly: true, Enabled: true, Size: 9}}, Status: model.MCPActive}
	if err := r.Create(ctx, s, sealed); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, s.ID)
	if err != nil || got.Name != "jira_docs" || got.Args[0] != "-a" || got.Roles[0] != "builder" || !got.Tools[0].Enabled || got.DiscoveredAt != nil {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	enc, _ := r.Secret(ctx, s.ID)
	if bytes.Contains(enc, []byte("s3cret")) {
		t.Fatal("secret stored in plaintext")
	}
	if plain, err := box.Open(enc); err != nil || string(plain) != `{"TOKEN":"s3cret"}` {
		t.Fatalf("open: %q %v", plain, err)
	}
	now := time.Now()
	got.DiscoveredAt, got.Status = &now, model.MCPDisabled
	if err := r.Update(ctx, got, nil); err != nil {
		t.Fatal(err)
	}
	if again, _ := r.Secret(ctx, s.ID); !bytes.Equal(again, enc) {
		t.Fatal("update with a nil secret must keep the stored one")
	}
	if g, _ := r.Get(ctx, s.ID); g.Status != model.MCPDisabled || g.DiscoveredAt == nil {
		t.Fatalf("update: %+v", g)
	}
	if err := r.Delete(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, s.ID); err != model.ErrMCPNotFound {
		t.Fatalf("after delete: %v", err)
	}
}

func TestMCPRepoRejectsBadAndDuplicateNames(t *testing.T) {
	r := &MCPRepo{DB: openTestDB(t)}
	ctx := context.Background()
	for _, name := range []string{"", "Upper", "has-dash", "sp ace", "dot.name", "a__b", "_a", "a_", "a_very_long_name_that_is_over_forty_characters_long"} {
		s := &model.MCPServerRec{Name: name, Transport: model.MCPHTTP, Auth: model.MCPAuthNone, Status: model.MCPActive}
		if err := r.Create(ctx, s, nil); err == nil {
			t.Fatalf("name %q must be rejected", name)
		}
	}
	ok := &model.MCPServerRec{Name: "ok_1", Transport: model.MCPHTTP, Auth: model.MCPAuthNone, Status: model.MCPActive}
	if err := r.Create(ctx, ok, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(ctx, &model.MCPServerRec{Name: "ok_1", Transport: model.MCPHTTP, Auth: model.MCPAuthNone, Status: model.MCPActive}, nil); err == nil {
		t.Fatal("duplicate name must be rejected")
	}
}

func TestMigration022KeepsMCPRowsAndConstraints(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, "migrations", 21); err != nil {
		t.Fatal(err)
	}
	const cols = `INSERT INTO mcp_servers (id, name, transport, command, args, url, auth, secret_enc, roles, tools, status, last_error, discovered_at, created_at, updated_at) VALUES `
	for _, q := range []string{
		cols + `('m1','jira_docs','http','','[]','https://x.example/mcp','token',x'0102','["builder"]','[{"name":"t"}]','active','boom',5,10,11)`,
		cols + `('m2','plain','stdio','/bin/x','["-a"]','','none',x'','[]','[]','disabled','',NULL,20,21)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := goose.UpTo(db, "migrations", 22); err != nil {
		t.Fatal(err)
	}
	var name, url, auth, status, roles, lastErr string
	var secret []byte
	var disc sql.NullInt64
	var created int64
	if err := db.QueryRow(`SELECT name, url, auth, secret_enc, roles, status, last_error, discovered_at, created_at FROM mcp_servers WHERE id='m1'`).
		Scan(&name, &url, &auth, &secret, &roles, &status, &lastErr, &disc, &created); err != nil {
		t.Fatal(err)
	}
	if name != "jira_docs" || url != "https://x.example/mcp" || auth != "token" || !bytes.Equal(secret, []byte{1, 2}) || roles != `["builder"]` ||
		status != "active" || lastErr != "boom" || disc.Int64 != 5 || created != 10 {
		t.Fatalf("token row changed: %s %s %s %x %s %s %s %v %d", name, url, auth, secret, roles, status, lastErr, disc, created)
	}
	if err := db.QueryRow(`SELECT auth, status, discovered_at FROM mcp_servers WHERE id='m2'`).Scan(&auth, &status, &disc); err != nil || auth != "none" || status != "disabled" || disc.Valid {
		t.Fatalf("none row changed: %s %s %v %v", auth, status, disc, err)
	}
	if _, err := db.Exec(cols + `('m3','oauth_srv','http','','[]','https://y.example/mcp','oauth',x'','[]','[]','needs_sign_in','',NULL,1,1)`); err != nil {
		t.Fatalf("oauth / needs_sign_in must be accepted: %v", err)
	}
	for name, q := range map[string]string{
		"junk auth":   cols + `('m4','a','http','','[]','','junk',x'','[]','[]','active','',NULL,1,1)`,
		"junk status": cols + `('m5','b','http','','[]','','none',x'','[]','[]','junk','',NULL,1,1)`,
		"bad name":    cols + `('m6','Bad Name','http','','[]','','none',x'','[]','[]','active','',NULL,1,1)`,
		"duplicate":   cols + `('m7','plain','http','','[]','','none',x'','[]','[]','active','',NULL,1,1)`,
	} {
		if _, err := db.Exec(q); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
