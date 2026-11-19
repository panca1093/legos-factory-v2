package usecase

import (
	"context"
	"errors"
	"testing"

	"legos-factory/backend/internal/model"
)

func TestCreateConnectionRejectsSharedScope(t *testing.T) {
	in := &Integrations{Secrets: plainSealer{}}
	for _, role := range []model.Role{model.RoleAdmin, model.RoleEngineer} {
		_, err := in.Create(context.Background(), &model.User{ID: "u", Role: role},
			model.CreateConnectionRequest{Provider: model.ProviderGitHub, Name: "org", Scope: "shared", Token: "t"})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("role %v: shared scope must be invalid, got %v", role, err)
		}
	}
}
