package http

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
)

func TestKBScopeHidesRepoDocsFromListButNotAgents(t *testing.T) {
	r := bootKB(t)
	ctx := context.Background()
	put := func(as, id, body string) (int, []byte) {
		return r.do(t, as, "PUT", "/api/v1/projects/"+id+"/kb-scope", body)
	}
	list := func(q string) []string {
		_, body := r.do(t, "eng", "GET", "/api/v1/kb/docs?project_id=p1"+q, "")
		return docIDs(t, body)
	}

	if code, _ := put("viewer", "p1", `{"kb_exclude":["docs/**"]}`); code != 403 {
		t.Fatalf("viewer: %d", code)
	}
	if code, _ := put("outsider", "p1", `{"kb_exclude":["docs/**"]}`); code != 404 {
		t.Fatalf("non-member: %d", code)
	}
	for _, bad := range []string{`{"kb_exclude":["/etc/**"]}`, `{"kb_include":["../x"]}`} {
		if code, _ := put("eng", "p1", bad); code != 400 {
			t.Fatalf("%s: %d", bad, code)
		}
	}

	code, body := put("eng", "p1", `{"kb_include":[" "],"kb_exclude":["docs/**"]}`)
	var pr model.Project
	if err := json.Unmarshal(body, &pr); err != nil || code != 200 || len(pr.KBInclude) != 0 || len(pr.KBExclude) != 1 {
		t.Fatalf("round trip: %d %s", code, body)
	}
	_, body = r.do(t, "eng", "GET", "/api/v1/projects/p1", "")
	pr = model.Project{}
	if err := json.Unmarshal(body, &pr); err != nil || len(pr.KBExclude) != 1 || pr.KBExclude[0] != "docs/**" {
		t.Fatalf("project read: %s", body)
	}

	if ids := list(""); has(ids, repoID) || !has(ids, fwID) {
		t.Fatalf("browse must hide the excluded repo doc: %v", ids)
	}
	if ids := list("&q=integers+money"); has(ids, repoID) {
		t.Fatalf("search must hide the excluded repo doc: %v", ids)
	}
	ctxText, _ := r.know.Context(ctx, "p1", "builder", "", "integers money", r.repo, "")
	if !strings.Contains(ctxText, "docs/money.md") {
		t.Fatalf("agents still get the excluded doc as a pointer: %s", ctxText)
	}

	put("eng", "p1", `{"kb_include":["docs/**"],"kb_exclude":["docs/money.md"]}`)
	if ids := list(""); has(ids, repoID) {
		t.Fatalf("exclude wins over include: %v", ids)
	}
	put("eng", "p1", `{"kb_include":["docs/**"]}`)
	if ids := list(""); !has(ids, repoID) {
		t.Fatalf("include match shows the doc: %v", ids)
	}
	put("eng", "p1", `{"kb_include":["src/**"]}`)
	if ids := list(""); has(ids, repoID) {
		t.Fatalf("include without a match hides the doc: %v", ids)
	}
	put("eng", "p1", `{}`)
	if ids := list(""); !has(ids, repoID) {
		t.Fatalf("no config leaves the list unchanged: %v", ids)
	}
}
