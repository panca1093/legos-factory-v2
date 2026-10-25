package usecase

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

// dirSandbox reports every file under the workspace as changed.
type dirSandbox struct{}

func (dirSandbox) Prepare(context.Context, string, string, *model.GitAuth) (string, error) {
	return "", nil
}
func (dirSandbox) RunCheck(context.Context, string, model.Check, time.Duration) model.CheckResult {
	return model.CheckResult{}
}
func (dirSandbox) Commit(context.Context, string, string) (string, error) { return "", nil }
func (dirSandbox) PushBranch(context.Context, string, string, *model.GitAuth) (string, error) {
	return "", nil
}
func (dirSandbox) ChangedFiles(_ context.Context, dir string) ([]string, error) {
	var files []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			files = append(files, rel)
		}
		return nil
	})
	sort.Strings(files)
	return files, nil
}

// writingHarness writes content to file in the workspace, or nothing when file is empty.
type writingHarness struct{ file, content string }

func (writingHarness) Name() string { return "fake" }
func (writingHarness) Start(context.Context, model.AgentRequest) (model.AgentSession, error) {
	return fakeSession{}, nil
}
func (writingHarness) Cancel(context.Context, string) error { return nil }
func (h writingHarness) Result(context.Context, string) (model.AgentResult, error) {
	return model.AgentResult{Text: "done"}, nil
}

type writeOnStart struct {
	writingHarness
	dir *string
}

func (h writeOnStart) Start(context.Context, model.AgentRequest) (model.AgentSession, error) {
	if h.file != "" {
		p := filepath.Join(*h.dir, h.file)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(h.content), 0o644); err != nil {
			return nil, err
		}
	}
	return fakeSession{}, nil
}

func TestRunBuilderRequiresFileChanges(t *testing.T) {
	cases := []struct {
		name     string
		seed     map[string]string
		file     string
		content  string
		wantFail bool
	}{
		{"nothing written", nil, "", "", true},
		{"new file", nil, "a.go", "package a\n", false},
		{"earlier task already did the work", map[string]string{"a.go": "package a\n"}, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var dir string
			h := writeOnStart{writingHarness{tc.file, tc.content}, &dir}
			f, wo, wos := monitorForeman(t, h)
			if err := wos.ReplacePlan(context.Background(), wo.ID, []model.Task{{ID: "T1", WOID: wo.ID, Title: "t", Role: "builder"}}); err != nil {
				t.Fatal(err)
			}
			f.Sandbox = dirSandbox{}
			f.DataDir = t.TempDir()
			dir = f.workspaceDir(wo.ID)
			for name, content := range tc.seed {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, _, err := f.runBuilder(context.Background(), wo, model.Job{TaskID: "T1"}, "p", "m")
			var sawNoChanges bool
			for _, e := range agentEvents(t, wos) {
				if e.Kind == "task_no_changes" {
					sawNoChanges = true
					if e.Actor != "builder" || e.Message != "Builder finished task T1 without changing any files" {
						t.Fatalf("event = %s %q", e.Actor, e.Message)
					}
				}
			}
			if tc.wantFail {
				if !errors.Is(err, model.ErrPermanent) || !sawNoChanges {
					t.Fatalf("err = %v, task_no_changes = %v; want permanent failure with event", err, sawNoChanges)
				}
				return
			}
			if err != nil || sawNoChanges {
				t.Fatalf("err = %v, task_no_changes = %v; want success", err, sawNoChanges)
			}
		})
	}
}
