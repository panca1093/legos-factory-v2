package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func testSandbox(t *testing.T) Sandbox {
	t.Helper()
	rt := Local{Root: t.TempDir()}
	ws, err := rt.CreateWorkspace(context.Background(), "wo")
	if err != nil {
		t.Fatal(err)
	}
	sb, err := rt.Start(context.Background(), ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	return sb
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConflictMarkerDetection(t *testing.T) {
	ctx := context.Background()
	sb := testSandbox(t)
	dir := t.TempDir()
	write(t, dir, "full.txt", "a\n<<<<<<< HEAD\nx\n=======\ny\n>>>>>>> branch\n")
	write(t, dir, "mid.txt", "a\n=======\nb\n")
	write(t, dir, "tail.txt", "a\n>>>>>>> branch\n")
	write(t, dir, "clean.txt", "a\n==== not a marker\n")
	cases := []struct {
		files []string
		want  bool
	}{
		{[]string{"full.txt"}, true},
		{[]string{"mid.txt"}, true},
		{[]string{"tail.txt"}, true},
		{[]string{"clean.txt"}, false},
		{[]string{"clean.txt", "mid.txt"}, true},
		{nil, false},
	}
	for _, c := range cases {
		got, err := HasConflictMarkers(ctx, sb, dir, c.files)
		if err != nil || got != c.want {
			t.Errorf("%v = %v, %v", c.files, got, err)
		}
	}
	if _, err := HasConflictMarkers(ctx, sb, dir, []string{"missing.txt"}); err == nil {
		t.Error("a grep failure must surface as an error, not as no markers")
	}
}

func TestUnresolvableConflicts(t *testing.T) {
	ctx := context.Background()
	sb := testSandbox(t)
	dir := t.TempDir()
	write(t, dir, "text.txt", "a\n")
	write(t, dir, "bin.dat", "a\x00b\n")
	write(t, dir, "empty.txt", "")
	got, err := UnresolvableConflicts(ctx, sb, dir, []string{"text.txt", "bin.dat", "gone.txt", "empty.txt"})
	if err != nil || !reflect.DeepEqual(got, []string{"bin.dat", "gone.txt", "empty.txt"}) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestBranchDiffAndLeaseRejection(t *testing.T) {
	ctx := context.Background()
	origin := newOrigin(t)
	sb := testSandbox(t)
	if err := Clone(ctx, sb, origin, nil, "main"); err != nil {
		t.Fatal(err)
	}
	dir := sb.Workspace().RepoDir()
	if files, err := BranchDiff(ctx, sb, dir, "origin/main"); err != nil || len(files) != 0 {
		t.Fatalf("fresh clone diff = %v, %v", files, err)
	}
	write(t, dir, "new.txt", "n\n")
	if _, _, err := CommitAll(ctx, sb, dir, "work"); err != nil {
		t.Fatal(err)
	}
	if files, err := BranchDiff(ctx, sb, dir, "origin/main"); err != nil || !reflect.DeepEqual(files, []string{"new.txt"}) {
		t.Fatalf("diff = %v, %v", files, err)
	}
	if err := Push(ctx, sb, dir, "lf/x", nil); err != nil {
		t.Fatal(err)
	}
	sh(t, origin, "git", "update-ref", "refs/heads/lf/x", sh(t, origin, "git", "rev-parse", "main"))
	err := PushLease(ctx, sb, dir, "lf/x", sh(t, dir, "git", "rev-parse", "HEAD"), nil)
	if !IsLeaseRejection(err) {
		t.Fatalf("lease push after outside change = %v", err)
	}
	if IsLeaseRejection(errors.New("git push: exit 128: could not resolve host")) || IsLeaseRejection(nil) {
		t.Error("unrelated push errors are not lease rejections")
	}
}
