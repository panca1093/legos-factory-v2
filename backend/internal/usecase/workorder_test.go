package usecase

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

type cloneSandbox struct {
	dirSandbox
	dataDir   string
	files     map[string]string
	destroyed []string
}

func (s *cloneSandbox) Prepare(_ context.Context, woID, _ string, _ *model.GitAuth) (string, error) {
	dir := filepath.Join(s.dataDir, "workspaces", woID)
	for name, body := range s.files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func (s *cloneSandbox) Destroy(_ context.Context, woID string) error {
	s.destroyed = append(s.destroyed, woID)
	return nil
}

type fixedSettings struct{}

func (fixedSettings) Get(context.Context) (*model.Settings, error) { return &model.Settings{}, nil }

func createOrder(t *testing.T, files map[string]string) (*Foreman, *cloneSandbox, *model.WorkOrder, error) {
	t.Helper()
	f, _, wos := monitorForeman(t, nil)
	f.DataDir = t.TempDir()
	f.Proj = &store.ProjectRepo{DB: wos.DB}
	box := &cloneSandbox{dataDir: f.DataDir, files: files}
	f.Sandbox = box
	w := &WorkOrders{Store: wos, Projects: &Projects{Store: f.Proj}, Sandbox: box, Settings: fixedSettings{}}
	u := &model.User{ID: "u1", Role: model.RoleAdmin}
	wo, err := w.Create(context.Background(), u, model.CreateWorkOrderRequest{ProjectID: "p1", Title: "t", Objective: "o"})
	return f, box, wo, err
}

func TestCreateFreezesCheckProfileFromFreshClone(t *testing.T) {
	ctx := context.Background()
	f, _, wo, err := createOrder(t, map[string]string{"go.mod": "module x"})
	if err != nil {
		t.Fatal(err)
	}
	want := []model.Check{{Name: "go-test", Command: "go test ./..."}, {Name: "go-vet", Command: "go vet ./..."}}
	override := filepath.Join(f.workspaceDir(wo.ID), ".legos", "checks.json")
	if err := os.MkdirAll(filepath.Dir(override), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte(`{"checks":[{"name":"x","command":"true"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.workspaceDir(wo.ID), "go.mod")); err != nil {
		t.Fatal(err)
	}
	stored, err := f.WO.Get(ctx, wo.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.profile(ctx, stored)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Checks, want) {
		t.Fatalf("builder-edited workspace changed the checks: %+v", got.Checks)
	}
}

func TestCreateRejectsInvalidChecksFile(t *testing.T) {
	_, box, _, err := createOrder(t, map[string]string{"go.mod": "module x", ".legos/checks.json": "{nope"})
	if !errors.Is(err, ErrWorkspace) {
		t.Fatalf("want ErrWorkspace, got %v", err)
	}
	if len(box.destroyed) != 1 {
		t.Fatalf("clone not cleaned up: %v", box.destroyed)
	}
}
