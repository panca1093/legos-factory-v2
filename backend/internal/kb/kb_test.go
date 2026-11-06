package kb

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

func TestParseFrontmatterAndFallbacks(t *testing.T) {
	src := "---\ntype: ADR\ntitle: Money\nstatus: draft\nowner: me\nverified:\n  - {by: process:x, at: 2026-01-01}\n  - {by: \"human:ann\", at: 2026-01-02}\n---\n\n# Heading\n\nIntegers only.\nNo floats.\n\n## Sub\n\n```\n[x](code.md)\n```\n[a](../b/c.md#frag) [e](https://x.com/y.md) [s](self.md) [[Other]] [[deep/p|label]] [[missing]] [dup](../b/c.md)\n"
	p, err := Parse("repo", "p1", "docs/a/self.md", []byte(src), func(n string) (string, bool) {
		if n == "Other" {
			return "o.md", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "repo:p1:docs/a/self.md" || p.Type != "ADR" || p.Title != "Money" || p.Status != "draft" || p.Trust != model.KBTrustHuman {
		t.Fatalf("doc: %+v", p.KBDoc)
	}
	if p.Description != "Integers only. No floats." || p.Meta["owner"] != "me" || p.Meta["type"] != nil {
		t.Fatalf("desc/meta: %q %v", p.Description, p.Meta)
	}
	if p.Headings != "Heading\nSub" {
		t.Fatalf("headings %q", p.Headings)
	}
	want := []string{"repo:p1:docs/b/c.md", "repo:p1:o.md", "repo:p1:deep/p.md"}
	if fmt.Sprint(p.Links) != fmt.Sprint(want) {
		t.Fatalf("links %v want %v", p.Links, want)
	}
	if len(p.ContentHash) != 64 {
		t.Fatal("hash")
	}
}

func TestParseDefaults(t *testing.T) {
	p, err := Parse("repo", "", "x/notes.md", []byte("plain text only"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "notes" || p.Status != "stable" || p.Trust != model.KBTrustUnverified || p.Type != "" {
		t.Fatalf("%+v", p.KBDoc)
	}
	p, _ = Parse("repo", "", "a.md", []byte("---\nverified: {by: process:legos}\n---\n# T\n"), nil)
	if p.Title != "T" || p.Trust != model.KBTrustMachine {
		t.Fatalf("%+v", p.KBDoc)
	}
	long := strings.Repeat("word ", 100)
	p, _ = Parse("repo", "", "a.md", []byte("# T\n\n"+long), nil)
	if len([]rune(p.Description)) != 200 {
		t.Fatalf("desc len %d", len(p.Description))
	}
	if _, err := Parse("repo", "", "a.md", []byte("---\n: : [\n---\n"), nil); err == nil {
		t.Fatal("want yaml error")
	}
}

func cosine(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i] * b[i])
	}
	return s
}

func TestHashEmbedder(t *testing.T) {
	e := HashEmbedder{}
	if e.Name() != "hash-ngram-v1" || e.Dim() != 512 {
		t.Fatal("identity")
	}
	a, b, c := e.Embed("rounding change down"), e.Embed("round the change down"), e.Embed("kubernetes deployment manifest")
	if cosine(a, b) <= cosine(a, c) {
		t.Fatalf("similar %f unrelated %f", cosine(a, b), cosine(a, c))
	}
	if math.Abs(cosine(a, a)-1) > 1e-5 {
		t.Fatal("not normalised")
	}
	if fmt.Sprint(a) != fmt.Sprint(e.Embed("rounding change down")) {
		t.Fatal("not deterministic")
	}
	for _, v := range e.Embed("") {
		if v != 0 {
			t.Fatal("empty must be zero")
		}
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return string(out)
}

func TestBundleSeedWriteRemove(t *testing.T) {
	root := filepath.Join(t.TempDir(), "kb")
	b, err := OpenBundle(root, true)
	if err != nil {
		t.Fatal(err)
	}
	seed := b.Head()
	if seed == "" || !b.HasDefault("playbooks/inspector/ui_ux.md") || b.HasDefault("nope.md") {
		t.Fatal("seed/default")
	}
	b2, err := OpenBundle(root, true)
	if err != nil || b2.Head() != seed {
		t.Fatalf("reseed must not commit: %v", err)
	}
	front, body, err := b.Read("playbooks/foreman/escalation.md")
	if err != nil || front["role"] != "foreman" || !strings.Contains(body, "Escalate when") {
		t.Fatalf("read: %v %v", front, err)
	}
	sha, err := b.Write("projects/p1/journal/wo1/001-note.md", map[string]any{"type": "Journal Entry", "title": "Note", "description": "d", "work_order": "wo1"}, "# Note\n\nbody", "add note")
	if err != nil || sha != b.Head() || sha == seed {
		t.Fatal(err)
	}
	if n := strings.Count(gitOut(t, root, "log", "--format=%an"), "legos-factory"); n != 2 {
		t.Fatalf("commits by factory: %d", n)
	}
	idx, _ := os.ReadFile(filepath.Join(root, "projects/p1/journal/wo1/index.md"))
	if !strings.Contains(string(idx), "# wo1\n") || !strings.Contains(string(idx), "* [Note](001-note.md) - d") {
		t.Fatalf("dir index:\n%s", idx)
	}
	top, _ := os.ReadFile(filepath.Join(root, "index.md"))
	if !strings.Contains(string(top), "* [projects/](projects/) - 1 documents") || !strings.Contains(string(top), "* [playbooks/](playbooks/) - 8 documents") {
		t.Fatalf("root index:\n%s", top)
	}
	lg, _ := os.ReadFile(filepath.Join(root, "log.md"))
	l := string(lg)
	if !strings.HasPrefix(l, "# Log\n\n## ") || strings.Index(l, "add note") > strings.Index(l, "Seeded default playbooks") || strings.Count(l, "## 20") != 1 {
		t.Fatalf("log:\n%s", l)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "projects/p1/journal/wo1/001-note.md"))
	if !strings.HasPrefix(string(raw), "---\ntype: Journal Entry\ntitle: Note\ndescription: d\nwork_order: wo1\n---\n\n# Note") {
		t.Fatalf("file:\n%s", raw)
	}

	if _, err := b.Remove("projects/p1/journal/wo1/001-note.md", "drop note"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "projects")); !os.IsNotExist(err) {
		t.Fatal("empty dirs must be pruned")
	}
}

func TestBundleResetAndRejects(t *testing.T) {
	b, err := OpenBundle(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	const p = "playbooks/inspector/testing.md"
	orig, _ := os.ReadFile(filepath.Join(b.Root(), p))
	if _, err := b.Write(p, map[string]any{"title": "x"}, "custom", "edit"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ResetDefault(p, "reset"); err != nil {
		t.Fatal(err)
	}
	if now, _ := os.ReadFile(filepath.Join(b.Root(), p)); string(now) != string(orig) {
		t.Fatal("reset did not restore the default")
	}
	if _, err := b.ResetDefault("notes/none.md", "reset"); err == nil {
		t.Fatal("want no-default error")
	}
	for _, bad := range []string{"../x.md", "/abs.md", "a/../../x.md", "index.md", "a/log.md", ".git/x.md", "a.txt", "."} {
		if _, err := b.Write(bad, nil, "x", "m"); err == nil {
			t.Errorf("Write(%q) must fail", bad)
		}
		if _, err := b.Remove(bad, "m"); err == nil {
			t.Errorf("Remove(%q) must fail", bad)
		}
	}
}

func TestBundleAppendWritesProjectLogOnly(t *testing.T) {
	root := t.TempDir()
	b, err := OpenBundle(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func(orig func() time.Time) { now = orig }(now)
	day := func(s string) {
		now = func() time.Time { d, _ := time.ParseInLocation("2006-01-02 15:04", s, jakarta); return d }
	}

	day("2026-11-05 23:50")
	if _, err := b.Append("projects/p1/log.md", Clock(), "* 23:50 `a` **k** (x) — first", "m1"); err != nil {
		t.Fatal(err)
	}
	day("2026-11-06 00:10")
	if _, err := b.Append("projects/p1/log.md", Clock(), "* 00:10 `a`\n **k** (x) — second", "m2"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Append("projects/p1/log.md", Clock(), "* 00:11 `a` **k** (x) — third", "m3"); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(filepath.Join(root, "projects", "p1", "log.md"))
	want := "# Log\n\n## 2026-11-06\n\n* 00:11 `a` **k** (x) — third\n* 00:10 `a` **k** (x) — second\n\n## 2026-11-05\n\n* 23:50 `a` **k** (x) — first\n"
	if string(got) != want {
		t.Fatalf("project log:\n%s\nwant:\n%s", got, want)
	}
	if _, err := os.Stat(filepath.Join(root, "log.md")); !os.IsNotExist(err) {
		t.Fatalf("root log must not be touched by Append: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "projects", "p1", "index.md")); !os.IsNotExist(err) {
		t.Fatalf("Append must not generate an index: %v", err)
	}
	if out, _ := b.git("log", "--format=%s"); out != "m3\nm2\nm1\n" {
		t.Fatalf("one commit per append: %q", out)
	}

	if _, err := b.Write("projects/p1/doc.md", map[string]any{"title": "d"}, "x", "wrote doc"); err != nil {
		t.Fatal(err)
	}
	if root, _ := os.ReadFile(filepath.Join(root, "log.md")); strings.Count(string(root), "* **Update**") != 1 {
		t.Fatalf("Write still logs once to the root log:\n%s", root)
	}
	for _, bad := range []string{"log.md", "projects/log.md", "projects/p1/x.md", "projects/../log.md", "projects/p1/sub/log.md", "../log.md"} {
		if _, err := b.Append(bad, Clock(), "x", "m"); err == nil {
			t.Fatalf("Append(%q) should be refused", bad)
		}
	}
}

func TestBundleConcurrentWrites(t *testing.T) {
	b, err := OpenBundle(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	const n = 12
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := b.Write(fmt.Sprintf("d/%d.md", i), map[string]any{"title": "t"}, "x", fmt.Sprintf("w%d", i)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := strings.Count(gitOut(t, b.Root(), "log", "--oneline"), "\n"); got != n {
		t.Fatalf("commits %d want %d", got, n)
	}
	if gitOut(t, b.Root(), "status", "--porcelain") != "" {
		t.Fatal("dirty tree")
	}
}

type fakeIndex struct {
	docs    map[string]model.KBParsed
	touched []string
	deleted []string
}

func (f *fakeIndex) Hashes(_ context.Context, _, _ string) (map[string]string, error) {
	m := map[string]string{}
	for _, d := range f.docs {
		m[d.Path] = d.ContentHash
	}
	return m, nil
}
func (f *fakeIndex) Upsert(_ context.Context, d model.KBParsed, _ []float32) error {
	f.docs[d.ID] = d
	return nil
}
func (f *fakeIndex) Touch(_ context.Context, id, _ string) error {
	f.touched = append(f.touched, id)
	return nil
}
func (f *fakeIndex) Delete(_ context.Context, id string) error {
	delete(f.docs, id)
	f.deleted = append(f.deleted, id)
	return nil
}
func (f *fakeIndex) Projects(_ context.Context, source string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, d := range f.docs {
		if d.Source == source && d.ProjectID != "" && !seen[d.ProjectID] {
			seen[d.ProjectID] = true
			out = append(out, d.ProjectID)
		}
	}
	return out, nil
}
func (f *fakeIndex) Get(context.Context, string) (*model.KBDoc, error) { return nil, nil }
func (f *fakeIndex) Search(context.Context, model.KBQuery, []float32) ([]model.KBHit, error) {
	return nil, nil
}
func (f *fakeIndex) List(context.Context, model.KBQuery) ([]model.KBDoc, error) { return nil, nil }
func (f *fakeIndex) Backlinks(context.Context, string) ([]model.KBDoc, error)   { return nil, nil }

func put(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSyncPlainDir(t *testing.T) {
	root := t.TempDir()
	put(t, root, "a.md", "# A\n\nsee [[b]]\n")
	put(t, root, "docs/b.md", "# B\n")
	put(t, root, "index.md", "# Index\n")
	put(t, root, "node_modules/x/readme.md", "# no\n")
	put(t, root, "vendor/y.md", "# no\n")
	put(t, root, "big.md", strings.Repeat("x", 300<<10))
	put(t, root, "notes.txt", "no")
	idx := &fakeIndex{docs: map[string]model.KBParsed{}}
	ctx := context.Background()

	st, err := Sync(ctx, idx, HashEmbedder{}, "repo", "p", root, "c1")
	if err != nil || st != (SyncStats{Docs: 3, Changed: 3}) {
		t.Fatalf("%+v %v", st, err)
	}
	a := idx.docs["repo:p:a.md"]
	if a.CommitSHA != "c1" || len(a.Links) != 1 || a.Links[0] != "repo:p:docs/b.md" {
		t.Fatalf("a: %+v", a)
	}
	put(t, root, "docs/b.md", "# B2\n")
	os.Remove(filepath.Join(root, "index.md"))
	st, err = Sync(ctx, idx, HashEmbedder{}, "repo", "p", root, "c2")
	if err != nil || st != (SyncStats{Docs: 2, Changed: 1, Removed: 1}) || len(idx.touched) != 1 || idx.touched[0] != "repo:p:a.md" {
		t.Fatalf("%+v %v %v", st, err, idx.touched)
	}
}

func TestSyncGitFactorySkipsReserved(t *testing.T) {
	root := t.TempDir()
	gitOut(t, root, "init", "-q")
	put(t, root, "keep.md", "# Keep\n")
	put(t, root, "index.md", "# I\n")
	put(t, root, "sub/log.md", "# L\n")
	put(t, root, "ignored/x.md", "# X\n")
	put(t, root, ".gitignore", "ignored/\n")
	gitOut(t, root, "add", "keep.md", "index.md", "sub/log.md")
	idx := &fakeIndex{docs: map[string]model.KBParsed{}}
	st, err := Sync(context.Background(), idx, HashEmbedder{}, "factory", "", root, "s")
	if err != nil || st.Docs != 1 || idx.docs["factory::keep.md"].Title != "Keep" {
		t.Fatalf("factory: %+v %v", st, err)
	}
	idx = &fakeIndex{docs: map[string]model.KBParsed{}}
	st, _ = Sync(context.Background(), idx, HashEmbedder{}, "repo", "p", root, "s")
	if st.Docs != 3 {
		t.Fatalf("repo should index index.md and log.md and skip ignored: %+v", st)
	}
}

func TestSyncFactoryNeverIndexesJournal(t *testing.T) {
	b, err := OpenBundle(filepath.Join(t.TempDir(), "kb"), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.WriteMany([]File{
		{Path: "projects/p1/journal/wo_1/001-plan.md", Front: map[string]any{"type": "Journal Entry"}, Body: "# J\n"},
		{Path: "projects/p2/journal/wo_2/001-plan.md", Front: map[string]any{"type": "Journal Entry"}, Body: "# J\n"},
		{Path: "projects/p2/notes/n.md", Front: map[string]any{"type": "Note"}, Body: "# N\n"},
	}, "seed"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	idx := &fakeIndex{docs: map[string]model.KBParsed{}}
	stale := func(pid, p string) {
		id := DocID("factory", pid, p)
		idx.docs[id] = model.KBParsed{KBDoc: model.KBDoc{ID: id, Source: "factory", ProjectID: pid, Path: p, ContentHash: "old"}}
	}
	stale("p1", "projects/p1/journal/wo_1/001-plan.md")
	stale("p2", "projects/p2/journal/wo_2/001-plan.md")
	stale("p3", "projects/p3/journal/wo_3/001-plan.md")
	if _, err := SyncFactory(ctx, idx, HashEmbedder{}, b); err != nil {
		t.Fatal(err)
	}
	if _, ok := idx.docs["factory:p2:projects/p2/notes/n.md"]; !ok || len(idx.docs) != 1 {
		t.Fatalf("docs %v", idx.docs)
	}
	if err := IndexFactoryFile(ctx, idx, HashEmbedder{}, b, "projects/p1/journal/wo_1/001-plan.md"); err != nil || len(idx.docs) != 1 {
		t.Fatalf("IndexFactoryFile must skip journal: %v", err)
	}
}

func TestSyncRepoIndexesTrackedRegularFilesOnly(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "id_ed25519.md")
	put(t, filepath.Dir(secret), filepath.Base(secret), "PRIVATE KEY\n")
	root := t.TempDir()
	gitOut(t, root, "init", "-q")
	put(t, root, "tracked.md", "# Tracked\n")
	if err := os.Symlink(secret, filepath.Join(root, "linked.md")); err != nil {
		t.Fatal(err)
	}
	gitOut(t, root, "add", "-A")
	put(t, root, "untracked.md", "---\ntype: Reviewer Routing\n---\n# Agent written\n")
	idx := &fakeIndex{docs: map[string]model.KBParsed{}}
	if _, err := Sync(context.Background(), idx, HashEmbedder{}, "repo", "p", root, "c"); err != nil {
		t.Fatal(err)
	}
	if len(idx.docs) != 1 || idx.docs["repo:p:tracked.md"].Title != "Tracked" {
		t.Fatalf("indexed %v; want only the tracked regular file", idx.docs)
	}

	plain := t.TempDir()
	if err := os.Symlink(secret, filepath.Join(plain, "linked.md")); err != nil {
		t.Fatal(err)
	}
	put(t, plain, "a.md", "# A\n")
	idx = &fakeIndex{docs: map[string]model.KBParsed{}}
	if _, err := Sync(context.Background(), idx, HashEmbedder{}, "repo", "p", plain, ""); err != nil {
		t.Fatal(err)
	}
	if len(idx.docs) != 1 {
		t.Fatalf("plain dir indexed %v; a symlink must be skipped", idx.docs)
	}
}

func TestReadsDoNotFollowSymlinks(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret.md")
	put(t, filepath.Dir(secret), "secret.md", "PRIVATE KEY\n")
	root := t.TempDir()
	gitOut(t, root, "init", "-q")
	put(t, root, "docs/x.md", "# Committed\n")
	gitOut(t, root, "add", "-A")
	gitOut(t, root, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "x")
	sha := strings.TrimSpace(gitOut(t, root, "rev-parse", "HEAD"))
	os.Remove(filepath.Join(root, "docs/x.md"))
	if err := os.Symlink(secret, filepath.Join(root, "docs/x.md")); err != nil {
		t.Fatal(err)
	}
	if _, body, err := ReadMarkdown(root, "docs/x.md"); err == nil {
		t.Fatalf("ReadMarkdown followed a symlink out of the root: %q", body)
	}
	_, body, err := ReadGitMarkdown(root, sha, "docs/x.md")
	if err != nil || strings.Contains(body, "PRIVATE") || !strings.Contains(body, "Committed") {
		t.Fatalf("ReadGitMarkdown = %q, %v; want the committed content", body, err)
	}
	if _, _, err := ReadGitMarkdown(root, "--output=x", "docs/x.md"); err == nil {
		t.Fatal("a non-hex commit must be rejected")
	}
}

func TestGitPlaybookPathsReadsTheCommitNotTheWorkingTree(t *testing.T) {
	dir := t.TempDir()
	gitOut(t, dir, "init", "--quiet")
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func() string {
		gitOut(t, dir, "add", "-A")
		gitOut(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--quiet", "-m", "c")
		return strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))
	}
	write("docs/b.md", "---\ntype: Playbook\nrole: inspector\n---\n\nB\n")
	write("a.md", "---\ntype: Playbook\nrole: foreman\n---\n\nA\n")
	write("docs/guide.md", "---\ntype: Guide\n---\n\nG\n")
	first := commit()
	write("docs/c.md", "---\ntype: Playbook\nrole: inspector\n---\n\nC\n")

	got, err := GitPlaybookPaths(dir, first)
	if err != nil || fmt.Sprint(got) != "[a.md docs/b.md]" {
		t.Fatalf("got %v, %v", got, err)
	}
	gitOut(t, dir, "rm", "-q", "-f", "docs/b.md", "a.md")
	empty := commit()
	got, err = GitPlaybookPaths(dir, empty)
	if err != nil || got == nil || len(got) != 1 || got[0] != "docs/c.md" {
		t.Fatalf("got %v, %v", got, err)
	}
	gitOut(t, dir, "rm", "-q", "-f", "docs/c.md")
	none := commit()
	got, err = GitPlaybookPaths(dir, none)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("no matches must be an empty slice, got %#v, %v", got, err)
	}
	if _, err := GitPlaybookPaths(dir, "HEAD"); err == nil {
		t.Fatal("a ref that is not a commit id must be rejected")
	}
}

func TestGitPlaybookPathsFindsQuotedTypesAndUnusualPathsButSkipsVendor(t *testing.T) {
	dir := t.TempDir()
	gitOut(t, dir, "init", "--quiet")
	for rel, content := range map[string]string{
		"docs/café.md":         "---\ntype: \"Playbook\"\nrole: foreman\n---\nx\n",
		"docs/tab rules.md":    "---\ntype:\tPlaybook\nrole: foreman\n---\nx\n",
		"vendor/lib/review.md": "---\ntype: Playbook\nrole: inspector\n---\nx\n",
	} {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--quiet", "-m", "c")
	sha := strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))
	got, err := GitPlaybookPaths(dir, sha)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"docs/café.md", "docs/tab rules.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}
