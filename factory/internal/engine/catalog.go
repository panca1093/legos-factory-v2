package engine

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"legos-factory/factory/internal/catalog"
	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/harness"
	"legos-factory/factory/internal/workflow"
)

const (
	maxPackBytes = 6000
	maxTreeBytes = 8000
)

// roleConfigured reports whether role has an explicit config or a bound profile.
func (e *Engine) roleConfigured(ctx context.Context, p Project, role harness.Role) bool {
	if p.Roles[string(role)].set() {
		return true
	}
	if e.Profiles != nil {
		_, err := e.Profiles.Resolve(ctx, p.ID, role)
		return err == nil
	}
	return false
}

// catalogPack returns the context pack for the base commit: cached, else deterministic plus an optional read-only
// Cataloger session. nil means no pack (phase disabled or nothing recognizable); the order proceeds without it.
func (e *Engine) catalogPack(ctx context.Context, o *orch, facts []fact.Fact) (*catalog.Pack, error) {
	if e.Catalog == nil {
		return nil, nil
	}
	commit, err := o.git(ctx, o.dir, "rev-parse", o.p.DefaultBranch)
	if err != nil {
		e.log().Warn("catalog: base commit", "wo", o.woID, "err", err)
		return nil, nil
	}
	if pack, ok, err := e.Catalog.Get(ctx, o.p.Repo, commit); err != nil {
		e.log().Warn("catalog: lookup", "wo", o.woID, "err", err)
		return nil, nil
	} else if ok {
		return &pack, e.catalogReady(ctx, o.woID, facts, pack, true)
	}
	pack := e.deterministicPack(ctx, o)
	if e.roleConfigured(ctx, o.p, harness.RoleCataloger) {
		got, ok, err := e.runCataloger(ctx, o)
		if err != nil {
			return nil, err
		}
		if ok {
			pack = catalog.Merge(pack, got)
		}
	}
	pack.Repo, pack.Commit = o.p.Repo, commit
	if len(pack.Files) == 0 {
		e.log().Warn("catalog: nothing recognizable", "wo", o.woID)
		return nil, nil
	}
	if err := e.Catalog.Put(ctx, pack); err != nil {
		e.log().Warn("catalog: store", "wo", o.woID, "err", err)
	}
	return &pack, e.catalogReady(ctx, o.woID, facts, pack, false)
}

func (e *Engine) catalogReady(ctx context.Context, woID string, facts []fact.Fact, p catalog.Pack, hit bool) error {
	for _, f := range facts {
		if f.Kind == fact.KindCatalogReady {
			if r, err := fact.Decode[fact.CatalogReady](f); err == nil && r.Commit == p.Commit && r.Repo == p.Repo {
				return nil
			}
		}
	}
	_, err := e.Facts.Append(ctx, woID, fact.KindCatalogReady, fact.CatalogReady{Repo: p.Repo, Commit: p.Commit, CacheHit: hit, Files: len(p.Files)})
	return err
}

// deterministicPack stages the manifest files on the host because the sandbox filesystem may not be host-visible.
func (e *Engine) deterministicPack(ctx context.Context, o *orch) catalog.Pack {
	tmp, err := os.MkdirTemp("", "lf-catalog-")
	if err != nil {
		return catalog.Pack{}
	}
	defer os.RemoveAll(tmp)
	for _, n := range []string{"go.mod", "package.json", "Makefile", "README.md", "README"} {
		if out, _, code, err := o.sb.Exec(ctx, o.dir, nil, "cat", n); err == nil && code == 0 {
			_ = os.WriteFile(filepath.Join(tmp, n), []byte(out), 0o644)
		}
	}
	run := func(ctx context.Context, _ string, args ...string) (string, error) {
		out, _, _, err := o.sb.Exec(ctx, o.dir, nil, args...)
		return out, err
	}
	p, err := catalog.Deterministic(ctx, run, tmp)
	if err != nil {
		return catalog.Pack{}
	}
	return p
}

// runCataloger returns ok=false when the reply stays unusable after one re-ask; the deterministic pack then stands alone.
func (e *Engine) runCataloger(ctx context.Context, o *orch) (catalog.Pack, bool, error) {
	tree, _ := o.git(ctx, o.dir, "ls-files")
	if len(tree) > maxTreeBytes {
		cut := maxTreeBytes
		if i := strings.LastIndex(tree[:cut], "\n"); i >= 0 {
			cut = i
		}
		tree = tree[:cut] + "\n…(truncated)"
	}
	prompt := catalog.Prompt(o.objective, tree)
	reply, err := o.runRole(ctx, harness.RoleCataloger, "cataloger", prompt, prompt)
	if err != nil {
		return catalog.Pack{}, false, err
	}
	p, perr := catalog.Parse(reply)
	if perr == nil {
		return p, true, nil
	}
	again := "Your reply could not be used (" + firstLine(perr.Error(), 200) + "). Reply with the JSON object in a final ```json block, as specified."
	if reply, err = o.runRole(ctx, harness.RoleCataloger, "cataloger", again, prompt+"\n\n"+again); err != nil {
		return catalog.Pack{}, false, err
	}
	if p, perr = catalog.Parse(reply); perr != nil {
		_, err = e.Facts.Append(ctx, o.woID, fact.KindRunStep, fact.RunStep{Step: "catalog_unparseable", Summary: firstLine(perr.Error(), 200)})
		return catalog.Pack{}, false, err
	}
	return p, true, nil
}

// packBlock wraps the rendered pack as untrusted data: it is derived from repository content.
func packBlock(pack *catalog.Pack) string {
	if pack == nil {
		return ""
	}
	n := newNonce()
	return "\n\nRepository context pack (a read-only survey of the base commit; trust the repository over it if they disagree). " +
		untrustedNote(n, "PACK") + "\n" + fence("PACK", n, catalog.Render(*pack, maxPackBytes))
}

func packTestPkg(pack *catalog.Pack) string {
	if pack == nil {
		return ""
	}
	return pack.Commands["test_pkg"]
}

var (
	readTools = []string{"read", "grep", "glob", "ls", "list", "search", "find", "view", "notebookread"}
	editTools = []string{"edit", "multiedit", "write", "patch", "apply_patch", "notebookedit", "create"}
)

// toolKind classifies a tool call by the first word of its title; shell calls are neither.
func toolKind(ev harness.Event) string {
	f := strings.Fields(ev.Tool)
	if len(f) == 0 {
		return ""
	}
	w := strings.ToLower(strings.TrimRight(f[0], ":"))
	switch {
	case slices.Contains(editTools, w):
		return "edit"
	case slices.Contains(readTools, w):
		return "read"
	}
	return ""
}

// lockfileSibling maps a lockfile to the manifest whose change explains it; "" means it is noise whenever not owned.
var lockfileSibling = map[string]string{
	"package-lock.json": "", "yarn.lock": "", "pnpm-lock.yaml": "", "npm-shrinkwrap.json": "",
	"go.sum": "go.mod", "Cargo.lock": "Cargo.toml", "poetry.lock": "pyproject.toml", "Pipfile.lock": "Pipfile",
	"composer.lock": "composer.json", "Gemfile.lock": "Gemfile",
}

// lockfileNoise lists changed lockfiles outside the owned paths whose manifest did not change alongside (lesson #7).
func lockfileNoise(files, owned []string) []string {
	var out []string
	for _, f := range files {
		sib, ok := lockfileSibling[path.Base(f)]
		if !ok || workflow.Owned(f, owned) {
			continue
		}
		if sib != "" && slices.Contains(files, path.Join(path.Dir(f), sib)) {
			continue
		}
		out = append(out, f)
	}
	return out
}

func (e *Engine) warnLockfileNoise(ctx context.Context, woID, run string, files, owned []string) error {
	noise := lockfileNoise(files, owned)
	if len(noise) == 0 {
		return nil
	}
	_, err := e.Facts.Append(ctx, woID, fact.KindRunStep, fact.RunStep{Run: run, Step: "lockfile_noise", Summary: "warning: lockfile changes outside the owned paths: " + strings.Join(noise, ", ")})
	return err
}
