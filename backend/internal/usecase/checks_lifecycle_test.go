package usecase

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

func TestApproveRecomputesHashesIgnoringClientValues(t *testing.T) {
	r := newSuggestRig(t, goodReply)
	u := &model.User{ID: "u1"}
	_ = r.s.Start(context.Background(), u, "p1")
	p := r.wait(t).Proposal.Profile
	real := map[string]string{}
	for _, s := range p.Sources {
		real[s.Path] = s.SHA256
	}
	for i := range p.Sources {
		p.Sources[i].SHA256 = "forged"
	}
	got, err := r.s.Approve(context.Background(), u, "p1", p)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got.Sources {
		if s.SHA256 != real[s.Path] || s.SHA256 == "forged" {
			t.Fatalf("source %s kept a client hash: %q want %q", s.Path, s.SHA256, real[s.Path])
		}
	}
}

func TestStatusNeverReadsTheRepositoryInline(t *testing.T) {
	r := newSuggestRig(t, goodReply)
	u := &model.User{ID: "u1"}
	_ = r.s.Start(context.Background(), u, "p1")
	if _, err := r.s.Approve(context.Background(), u, "p1", r.wait(t).Proposal.Profile); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	release := make(chan struct{})
	r.s.SourceDir = func(context.Context, string) (string, error) {
		calls.Add(1)
		<-release
		return r.repo, nil
	}
	done := make(chan *CheckStatus)
	go func() {
		st, _ := r.s.Status(context.Background(), u, "p1")
		done <- st
	}()
	select {
	case st := <-done:
		if st.Stale {
			t.Fatal("no cached verdict yet: stale must be false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Status blocked on the repository checkout")
	}
	close(release)
	r.s.Wait()
	for i := 0; i < 5; i++ {
		_, _ = r.s.Status(context.Background(), u, "p1")
	}
	r.s.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("repository read %d times within the TTL, want 1", n)
	}
}

func TestStopChecksCallsStopStackForMembersOnly(t *testing.T) {
	r := newSuggestRig(t)
	var stopped []string
	if err := r.s.Stop(context.Background(), &model.User{ID: "u1"}, "p1"); !errors.Is(err, ErrCheckSuggestUnavailable) {
		t.Fatalf("no stop hook: %v", err)
	}
	r.s.StopStack = func(_ context.Context, id string) error { stopped = append(stopped, id); return nil }
	if err := r.s.Stop(context.Background(), &model.User{ID: "u1"}, "p1"); err != nil || len(stopped) != 1 || stopped[0] != "p1" {
		t.Fatalf("err=%v stopped=%v", err, stopped)
	}
}

func TestDeleteProjectStopsStackAndRemovesItsDir(t *testing.T) {
	ctx := context.Background()
	f, wos := deleteFixture(t, model.StateCancelled)
	d := &fakeDocker{}
	f.Docker = d
	if err := f.Proj.Create(ctx, &model.Project{ID: "prj_9", Name: "x"}); err != nil {
		t.Fatal(err)
	}
	_ = wos
	stackDir := filepath.Join(f.DataDir, "stacks", "prj_9")
	if err := os.MkdirAll(stackDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteProject(ctx, "prj_9"); err != nil {
		t.Fatal(err)
	}
	if d.count("compose -p legos-ck-prj_9 down -v") != 1 {
		t.Fatalf("stack not stopped: %v", d.calls)
	}
	if exists(stackDir) {
		t.Fatal("stack dir survived")
	}
}

func TestDeleteProjectWithoutStackTouchesNoDocker(t *testing.T) {
	ctx := context.Background()
	f, _ := deleteFixture(t, model.StateCancelled)
	d := &fakeDocker{}
	f.Docker = d
	if err := f.Proj.Create(ctx, &model.Project{ID: "prj_8", Name: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteProject(ctx, "prj_8"); err != nil {
		t.Fatal(err)
	}
	if len(d.calls) != 0 {
		t.Fatalf("docker called for a project that never had services: %v", d.calls)
	}
}
