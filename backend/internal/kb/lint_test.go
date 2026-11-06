package kb

import (
	"os"
	"path/filepath"
	"testing"
)

var goodBundle = map[string]string{
	"index.md":          "---\nokf_version: \"0.1\"\n---\n\n# Knowledge\n\n* [a](a.md)\n",
	"log.md":            "# Log\n\n## 2026-11-06\n\n* **Update**: x\n",
	"a.md":              "---\ntype: Note\n---\n\n# A\n\n[b](sub/b.md) [web](https://x.com/y) [top](#top) [anchor](sub/b.md#h)\n",
	"sub/index.md":      "# sub\n\n* [B](b.md)\n",
	"sub/b.md":          "---\ntype: Spec\n---\n\n# B\n\n[a](../a.md)\n",
	"projects/p/log.md": "# Log\n\n## 2026-11-06\n\n* x\n",
	".git/ignored.md":   "no frontmatter [[x]]",
}

func lintFixture(t *testing.T, override map[string]string) []Violation {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{}
	for k, v := range goodBundle {
		files[k] = v
	}
	for k, v := range override {
		files[k] = v
	}
	for rel, c := range files {
		put(t, root, rel, c)
	}
	return Lint(root)
}

func TestLintCleanFixture(t *testing.T) {
	if vs := lintFixture(t, nil); len(vs) != 0 {
		t.Fatalf("want clean, got %+v", vs)
	}
}

func TestLintGates(t *testing.T) {
	cases := []struct {
		name, gate, rel, content string
	}{
		{"A no frontmatter", "A", "a.md", "# A\n"},
		{"A empty type", "A", "a.md", "---\ntype: \"\"\n---\n\n# A\n"},
		{"A bad yaml", "A", "a.md", "---\ntype: [\n---\n\n# A\n"},
		{"B extra key", "B", "index.md", "---\nokf_version: \"0.1\"\ntitle: x\n---\n\n# K\n"},
		{"B no frontmatter", "B", "index.md", "# K\n"},
		{"C sub-index frontmatter", "C", "sub/index.md", "---\ntype: Index\n---\n\n# sub\n"},
		{"D log heading", "D", "projects/p/log.md", "# Log\n\n## Nov 6\n"},
		{"D root log heading", "D", "log.md", "# Log\n\n## 2026-11-06 extra\n"},
		{"E wikilink", "E", "a.md", "---\ntype: Note\n---\n\nsee [[Other]]\n"},
		{"F file link", "F", "a.md", "---\ntype: Note\n---\n\n[x](file:///etc/x.md)\n"},
		{"F users link", "F", "a.md", "---\ntype: Note\n---\n\n[x](/Users/me/x.md)\n"},
		{"F home link", "F", "a.md", "---\ntype: Note\n---\n\n[x](/home/me/x.md)\n"},
		{"G leading slash", "G", "a.md", "---\ntype: Note\n---\n\n[x](/sub/b.md)\n"},
		{"G broken", "G", "a.md", "---\ntype: Note\n---\n\n[x](sub/missing.md#h)\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vs := lintFixture(t, map[string]string{c.rel: c.content})
			if len(vs) != 1 || vs[0].Gate != c.gate || vs[0].Path != c.rel {
				t.Fatalf("want one %s on %s, got %+v", c.gate, c.rel, vs)
			}
		})
	}
}

func TestLintRealBundle(t *testing.T) {
	root := filepath.Join(t.TempDir(), "kb")
	b, err := OpenBundle(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if vs := Lint(root); len(vs) != 0 {
		t.Fatalf("seeded bundle: %+v", vs)
	}
	if _, err := b.Write("projects/p1/notes/n.md", map[string]any{"type": "Note", "title": "N", "description": "d"}, "# N\n\nbody", "add note"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.WriteMany([]File{{Path: "projects/p1/brief/b.md", Front: map[string]any{"type": "Brief", "title": "B"}, Body: "# B\n"}}, "add brief"); err != nil {
		t.Fatal(err)
	}
	if vs := Lint(root); len(vs) != 0 {
		t.Fatalf("written bundle: %+v", vs)
	}
	if err := os.WriteFile(filepath.Join(root, "x.md"), []byte("# no front\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if vs := Lint(root); len(vs) != 1 || vs[0].Gate != "A" {
		t.Fatalf("want one A, got %+v", vs)
	}
}
