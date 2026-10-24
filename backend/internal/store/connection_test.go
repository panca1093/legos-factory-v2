package store

import (
	"bytes"
	"context"
	"testing"

	"legos-factory/backend/internal/model"
)

func TestSecretBoxRoundTripAndTamper(t *testing.T) {
	box, err := NewSecretBox(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal([]byte("ghp_token"))
	if err != nil || bytes.Contains(sealed, []byte("ghp_token")) {
		t.Fatalf("seal must not leave plaintext: %v", err)
	}
	if plain, err := box.Open(sealed); err != nil || string(plain) != "ghp_token" {
		t.Fatalf("open: %q %v", plain, err)
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := box.Open(sealed); err == nil {
		t.Fatal("tampered ciphertext must fail")
	}
	other, _ := NewSecretBox(bytes.Repeat([]byte{8}, 32))
	good, _ := box.Seal([]byte("x"))
	if _, err := other.Open(good); err == nil {
		t.Fatal("wrong key must fail")
	}
	if _, err := NewSecretBox([]byte("short")); err == nil {
		t.Fatal("short key must be rejected")
	}
}

func TestConnectionVisibilityAndVerification(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	users := &UserRepo{DB: db}
	for _, id := range []string{"u1", "u2"} {
		if err := users.Create(ctx, model.User{ID: id, Email: id + "@t", Name: id, Role: model.RoleEngineer}); err != nil {
			t.Fatal(err)
		}
	}
	repo := &ConnectionRepo{DB: db}
	shared := &model.Connection{Provider: "github", Name: "org", Scope: model.ScopeShared, OwnerID: "u1", BaseURL: "https://api.github.com"}
	mine := &model.Connection{Provider: "jira", Name: "mine", Scope: model.ScopePersonal, OwnerID: "u1", BaseURL: "https://x.atlassian.net", Username: "u1@t"}
	for _, c := range []*model.Connection{shared, mine} {
		if err := repo.Create(ctx, c, []byte("sealed")); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := repo.Visible(ctx, "u2"); len(got) != 1 || got[0].ID != shared.ID {
		t.Fatalf("u2 sees only the shared connection, got %+v", got)
	}
	if got, _ := repo.Visible(ctx, "u1"); len(got) != 2 || !got[0].HasCredential {
		t.Fatalf("owner sees both with credential flag, got %+v", got)
	}
	if err := repo.RecordCheck(ctx, shared.ID, "octocat", ""); err != nil {
		t.Fatal(err)
	}
	c, _ := repo.Get(ctx, shared.ID)
	if c.VerifiedAt == nil || c.Account != "octocat" {
		t.Fatalf("verification not recorded: %+v", c)
	}
	if err := repo.RotateSecret(ctx, shared.ID, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if c, _ := repo.Get(ctx, shared.ID); c.VerifiedAt != nil {
		t.Fatal("rotating the token must reset verification")
	}
	if err := repo.Delete(ctx, mine.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Secret(ctx, mine.ID); err != model.ErrConnectionNotFound {
		t.Fatalf("revoked connection must be gone: %v", err)
	}
}
