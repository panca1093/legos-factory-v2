package store_test

import (
	"context"
	"database/sql"
	"legos-factory/backend/internal/kb"
	"path/filepath"
	"reflect"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

var _ kb.Index = (*store.KBRepo)(nil)

func kbSetup(t *testing.T) (*store.KBRepo, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, id := range []string{"p1", "p2"} {
		if err := (&store.ProjectRepo{DB: db}).Create(context.Background(), &model.Project{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	return &store.KBRepo{DB: db}, db
}

func kbDoc(source, project, path, title, body string, links ...string) model.KBParsed {
	return model.KBParsed{
		KBDoc: model.KBDoc{
			ID: source + ":" + project + ":" + path, Source: source, ProjectID: project, Path: path,
			CommitSHA: "c1", ContentHash: "h-" + path, Type: "Doc", Status: model.KBStatusStable, Trust: model.KBTrustUnverified,
			Title: title, Links: links, Meta: map[string]any{},
		},
		Body: body,
	}
}

func kbUpsert(t *testing.T, r *store.KBRepo, vec []float32, docs ...model.KBParsed) {
	t.Helper()
	for _, d := range docs {
		if err := r.Upsert(context.Background(), d, vec); err != nil {
			t.Fatal(err)
		}
	}
}

func kbIDs(hits []model.KBHit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.Doc.ID)
	}
	return out
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestKBUpsertGetRoundTrip(t *testing.T) {
	r, _ := kbSetup(t)
	ctx := context.Background()
	d := kbDoc("repo", "p1", "docs/a.md", "Alpha", "alpha body", "repo:p1:docs/b.md", "repo:p1:docs/c.md")
	d.Description = "desc"
	d.Meta = map[string]any{"role": "foreman", "n": float64(3)}
	kbUpsert(t, r, []float32{1, 0}, d)

	got, err := r.Get(ctx, d.ID)
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.Title != "Alpha" || got.Description != "desc" || got.ProjectID != "p1" || got.CommitSHA != "c1" || got.IndexedAt.IsZero() {
		t.Fatalf("doc: %+v", got)
	}
	if !reflect.DeepEqual(got.Meta, d.Meta) {
		t.Fatalf("meta: %v", got.Meta)
	}
	if !reflect.DeepEqual(got.Links, []string{"repo:p1:docs/b.md", "repo:p1:docs/c.md"}) {
		t.Fatalf("links: %v", got.Links)
	}
	if missing, err := r.Get(ctx, "nope"); err != nil || missing != nil {
		t.Fatalf("missing: %v %v", missing, err)
	}

	fw := kbDoc("factory", "", "playbooks/x.md", "Factory", "x")
	kbUpsert(t, r, nil, fw)
	got, _ = r.Get(ctx, fw.ID)
	if got.ProjectID != "" {
		t.Fatalf("factory-wide project: %q", got.ProjectID)
	}
}

func TestKBReupsertReplacesFTSAndLinks(t *testing.T) {
	r, db := kbSetup(t)
	ctx := context.Background()
	d := kbDoc("repo", "p1", "a.md", "Alpha", "zebra crossing rules", "repo:p1:b.md")
	kbUpsert(t, r, []float32{1, 0}, d)
	d.Body, d.Links, d.ContentHash = "giraffe feeding rules", nil, "h2"
	kbUpsert(t, r, []float32{1, 0}, d)

	if hits, _ := r.Search(ctx, model.KBQuery{ProjectID: "p1", Text: "zebra"}, nil); len(hits) != 0 {
		t.Fatalf("old text still matches: %v", kbIDs(hits))
	}
	if hits, _ := r.Search(ctx, model.KBQuery{ProjectID: "p1", Text: "giraffe"}, nil); len(hits) != 1 {
		t.Fatalf("new text: %v", kbIDs(hits))
	}
	got, _ := r.Get(ctx, d.ID)
	if len(got.Links) != 0 || got.ContentHash != "h2" {
		t.Fatalf("doc: %+v", got)
	}
	if n := count(t, db, `SELECT count(*) FROM kb_fts_map`); n != 1 {
		t.Fatalf("map rows %d", n)
	}
}

func TestKBDeleteRemovesEverything(t *testing.T) {
	r, db := kbSetup(t)
	ctx := context.Background()
	d := kbDoc("repo", "p1", "a.md", "Alpha", "zebra", "repo:p1:b.md")
	kbUpsert(t, r, []float32{1, 0}, d)
	if err := r.Delete(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"kb_docs", "kb_links", "kb_vectors", "kb_fts_map"} {
		if n := count(t, db, `SELECT count(*) FROM `+tbl); n != 0 {
			t.Fatalf("%s has %d rows", tbl, n)
		}
	}
	if n := count(t, db, `SELECT count(*) FROM kb_fts WHERE kb_fts MATCH '"zebra"'`); n != 0 {
		t.Fatalf("fts still matches")
	}
}

func TestKBHashesScopeAndTouch(t *testing.T) {
	r, _ := kbSetup(t)
	ctx := context.Background()
	kbUpsert(t, r, nil,
		kbDoc("repo", "p1", "a.md", "A", "a"),
		kbDoc("repo", "p2", "b.md", "B", "b"),
		kbDoc("factory", "", "c.md", "C", "c"),
		kbDoc("factory", "p1", "d.md", "D", "d"))
	cases := []struct {
		source, project string
		want            map[string]string
	}{
		{"repo", "p1", map[string]string{"a.md": "h-a.md"}},
		{"repo", "", map[string]string{}},
		{"factory", "", map[string]string{"c.md": "h-c.md"}},
		{"factory", "p1", map[string]string{"d.md": "h-d.md"}},
	}
	for _, c := range cases {
		got, err := r.Hashes(ctx, c.source, c.project)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%s/%s: %v %v", c.source, c.project, got, err)
		}
	}
	if err := r.Touch(ctx, "repo:p1:a.md", "c2"); err != nil {
		t.Fatal(err)
	}
	if d, _ := r.Get(ctx, "repo:p1:a.md"); d.CommitSHA != "c2" {
		t.Fatalf("touch: %s", d.CommitSHA)
	}
}

func TestKBListFilters(t *testing.T) {
	r, _ := kbSetup(t)
	ctx := context.Background()
	draft := kbDoc("factory", "", "z-draft.md", "Draft", "d")
	draft.Status = model.KBStatusDraft
	dep := kbDoc("repo", "p1", "dep.md", "Dep", "d")
	dep.Status = model.KBStatusDeprecated
	pb := kbDoc("factory", "", "pb.md", "Playbook", "d")
	pb.Type = model.KBTypePlaybook
	kbUpsert(t, r, nil,
		kbDoc("repo", "p1", "b.md", "B", "b"), kbDoc("repo", "p1", "a.md", "A", "a"),
		kbDoc("repo", "p2", "other.md", "Other", "o"), pb, draft, dep)

	paths := func(q model.KBQuery) []string {
		docs, err := r.List(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, d := range docs {
			out = append(out, d.Path)
		}
		return out
	}
	if got := paths(model.KBQuery{ProjectID: "p1"}); !reflect.DeepEqual(got, []string{"pb.md", "a.md", "b.md"}) {
		t.Fatalf("p1 default: %v", got)
	}
	if got := paths(model.KBQuery{}); !reflect.DeepEqual(got, []string{"pb.md"}) {
		t.Fatalf("factory-wide only: %v", got)
	}
	if got := paths(model.KBQuery{ProjectID: "p1", Sources: []string{"repo"}}); !reflect.DeepEqual(got, []string{"a.md", "b.md"}) {
		t.Fatalf("sources: %v", got)
	}
	if got := paths(model.KBQuery{ProjectID: "p1", Types: []string{model.KBTypePlaybook}}); !reflect.DeepEqual(got, []string{"pb.md"}) {
		t.Fatalf("types: %v", got)
	}
	if got := paths(model.KBQuery{ProjectID: "p1", Statuses: []string{"draft", "deprecated"}}); !reflect.DeepEqual(got, []string{"z-draft.md", "dep.md"}) {
		t.Fatalf("statuses: %v", got)
	}
	if got := paths(model.KBQuery{ProjectID: "p1", Limit: 1}); len(got) != 1 {
		t.Fatalf("limit: %v", got)
	}
}

func TestKBSearchKeywordOnly(t *testing.T) {
	r, _ := kbSetup(t)
	kbUpsert(t, r, nil,
		kbDoc("repo", "p1", "money.md", "Money rules", "money is integer rupiah rounding"),
		kbDoc("repo", "p1", "ui.md", "UI", "buttons and layout"),
		kbDoc("repo", "p2", "leak.md", "Leak", "rupiah rounding in another project"),
		kbDoc("factory", "", "pb.md", "Playbook", "escalation rounding policy"))
	hits, err := r.Search(context.Background(), model.KBQuery{ProjectID: "p1", Text: "rupiah rounding"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := kbIDs(hits); !reflect.DeepEqual(got, []string{"repo:p1:money.md", "factory::pb.md"}) {
		t.Fatalf("hits: %v", got)
	}
	if hits[0].Score <= 0 {
		t.Fatalf("score %v", hits[0].Score)
	}
	if hits, _ := r.Search(context.Background(), model.KBQuery{ProjectID: "p1"}, nil); hits != nil {
		t.Fatalf("empty query: %v", kbIDs(hits))
	}
}

func TestKBSearchVectorOnlyAndFusedDeterministic(t *testing.T) {
	r, _ := kbSetup(t)
	ctx := context.Background()
	up := func(path, body string, vec []float32) {
		kbUpsert(t, r, vec, kbDoc("repo", "p1", path, path, body))
	}
	up("a.md", "alpha shared", []float32{1, 0})
	up("b.md", "beta shared", []float32{0.8, 0.6})
	up("c.md", "gamma", []float32{0, 1})
	up("novec.md", "delta", nil)

	hits, err := r.Search(ctx, model.KBQuery{ProjectID: "p1"}, []float32{1, 0})
	if err != nil {
		t.Fatal(err)
	}
	if got := kbIDs(hits); !reflect.DeepEqual(got, []string{"repo:p1:a.md", "repo:p1:b.md", "repo:p1:c.md"}) {
		t.Fatalf("vector-only: %v", got)
	}
	if zero, _ := r.Search(ctx, model.KBQuery{ProjectID: "p1"}, []float32{0, 0}); zero != nil {
		t.Fatalf("zero vec: %v", kbIDs(zero))
	}

	// keyword picks c.md first (only match "gamma"), vector prefers a.md; fused tie breaks by path.
	q := model.KBQuery{ProjectID: "p1", Text: "gamma"}
	first, _ := r.Search(ctx, q, []float32{1, 0})
	for i := 0; i < 3; i++ {
		again, _ := r.Search(ctx, q, []float32{1, 0})
		if !reflect.DeepEqual(kbIDs(first), kbIDs(again)) {
			t.Fatalf("non-deterministic: %v vs %v", kbIDs(first), kbIDs(again))
		}
	}
	// a: vector rank 1 -> 1/61; c: keyword rank 1 (1/61) + vector rank 3 (1/63) wins.
	if got := kbIDs(first); got[0] != "repo:p1:c.md" || got[1] != "repo:p1:a.md" {
		t.Fatalf("fused: %v", got)
	}
	if limited, _ := r.Search(ctx, model.KBQuery{ProjectID: "p1", Limit: 2}, []float32{1, 0}); len(limited) != 2 {
		t.Fatalf("limit: %d", len(limited))
	}
}

func TestKBSearchSeeAlsoAndStatusFilter(t *testing.T) {
	r, _ := kbSetup(t)
	ctx := context.Background()
	draft := kbDoc("repo", "p1", "l-draft.md", "Draft", "x")
	draft.Status = model.KBStatusDraft
	other := kbDoc("repo", "p2", "l-other.md", "Other project", "x")
	hit := kbDoc("repo", "p1", "hit.md", "Hit", "needle haystack",
		"repo:p1:l1.md", "repo:p1:l-draft.md", "repo:p2:l-other.md", "repo:p1:l2.md", "repo:p1:l3.md", "repo:p1:l4.md", "repo:p1:l5.md", "repo:p1:gone.md")
	kbUpsert(t, r, nil, hit, draft, other)
	for _, p := range []string{"l1", "l2", "l3", "l4", "l5"} {
		kbUpsert(t, r, nil, kbDoc("repo", "p1", p+".md", p, "unrelated"))
	}

	hits, err := r.Search(ctx, model.KBQuery{ProjectID: "p1", Text: "needle"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"repo:p1:hit.md", "repo:p1:l1.md", "repo:p1:l2.md", "repo:p1:l3.md", "repo:p1:l4.md"}
	if got := kbIDs(hits); !reflect.DeepEqual(got, want) {
		t.Fatalf("hits: %v", got)
	}
	for _, h := range hits[1:] {
		if h.SeeAlso != "repo:p1:hit.md" || h.Score != 0 {
			t.Fatalf("see-also: %+v", h)
		}
	}
	// drafts and deprecated never come back by default, even by keyword
	kbUpsert(t, r, nil, func() model.KBParsed {
		d := kbDoc("repo", "p1", "dep.md", "Dep", "needle")
		d.Status = model.KBStatusDeprecated
		return d
	}(), func() model.KBParsed { d := draft; d.Body = "needle"; return d }())
	hits, _ = r.Search(ctx, model.KBQuery{ProjectID: "p1", Text: "needle"}, nil)
	for _, h := range hits {
		if h.Doc.Status != model.KBStatusStable {
			t.Fatalf("non-stable returned: %+v", h.Doc)
		}
	}
	hits, _ = r.Search(ctx, model.KBQuery{ProjectID: "p1", Text: "needle", Statuses: []string{"draft"}}, nil)
	if got := kbIDs(hits); len(got) != 1 || got[0] != "repo:p1:l-draft.md" {
		t.Fatalf("draft opt-in: %v", got)
	}
}

func TestKBSearchOperatorInjectionHarmless(t *testing.T) {
	r, _ := kbSetup(t)
	kbUpsert(t, r, nil, kbDoc("repo", "p1", "a.md", "Alpha", "hello world"))
	for _, text := range []string{`hello" OR *`, `NEAR(hello world)`, `title:hello AND NOT`, `"`, `(((`, `hello*`} {
		if _, err := r.Search(context.Background(), model.KBQuery{ProjectID: "p1", Text: text}, nil); err != nil {
			t.Fatalf("%q: %v", text, err)
		}
	}
}

func TestKBBacklinks(t *testing.T) {
	r, _ := kbSetup(t)
	target := kbDoc("repo", "p1", "t.md", "T", "t")
	z := kbDoc("repo", "p1", "z.md", "Z", "z", target.ID)
	a := kbDoc("repo", "p1", "a.md", "A", "a", target.ID)
	a.Status = model.KBStatusDraft
	kbUpsert(t, r, nil, target, z, a, kbDoc("repo", "p1", "m.md", "M", "m"))
	got, err := r.Backlinks(context.Background(), target.ID)
	if err != nil || len(got) != 2 || got[0].Path != "a.md" || got[1].Path != "z.md" {
		t.Fatalf("backlinks: %+v %v", got, err)
	}
}

func TestKBRebuildGivesSameSearch(t *testing.T) {
	r, db := kbSetup(t)
	ctx := context.Background()
	docs := []model.KBParsed{
		kbDoc("repo", "p1", "a.md", "A", "rounding rupiah", "repo:p1:b.md"),
		kbDoc("repo", "p1", "b.md", "B", "rounding policy"),
		kbDoc("factory", "", "c.md", "C", "rupiah escalation"),
	}
	vecs := [][]float32{{1, 0}, {0.6, 0.8}, {0, 1}}
	build := func() []model.KBHit {
		for i, d := range docs {
			if err := r.Upsert(ctx, d, vecs[i]); err != nil {
				t.Fatal(err)
			}
		}
		hits, err := r.Search(ctx, model.KBQuery{ProjectID: "p1", Text: "rupiah rounding"}, []float32{1, 0})
		if err != nil {
			t.Fatal(err)
		}
		for i := range hits {
			hits[i].Doc.IndexedAt = hits[i].Doc.IndexedAt.Truncate(0)
		}
		return hits
	}
	before := build()
	for _, d := range docs {
		if err := r.Delete(ctx, d.ID); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, db, `SELECT count(*) FROM kb_fts_map`); n != 0 {
		t.Fatalf("map rows %d", n)
	}
	after := build()
	if len(before) == 0 || !reflect.DeepEqual(kbIDs(before), kbIDs(after)) {
		t.Fatalf("before %v after %v", kbIDs(before), kbIDs(after))
	}
	for i := range before {
		if before[i].Score != after[i].Score || before[i].SeeAlso != after[i].SeeAlso {
			t.Fatalf("hit %d differs: %+v vs %+v", i, before[i], after[i])
		}
	}
}
