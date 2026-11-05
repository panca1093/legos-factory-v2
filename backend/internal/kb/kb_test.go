package kb

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
	if !strings.Contains(string(top), "* [projects/](projects/) - 1 documents") || !strings.Contains(string(top), "* [playbooks/](playbooks/) - 7 documents") {
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
