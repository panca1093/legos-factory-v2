package store

import (
	"bytes"
	"context"
	"testing"
	"time"

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
	for _, name := range []string{"", "Upper", "has-dash", "sp ace", "dot.name", "a_very_long_name_that_is_over_forty_characters_long"} {
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
