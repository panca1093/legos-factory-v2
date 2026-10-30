package engine

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"legos-factory/factory/internal/catalog"
	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/harness"
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

const (
	maxManifestBytes  = 64 << 10
	catalogRetryAfter = 24 * time.Hour
)

// catalogLock serialises cataloging per (repo, commit) within the process, so concurrent work orders share one run.
func (e *Engine) catalogLock(ctx context.Context, key string) (func(), error) {
	v, _ := e.catLocks.LoadOrStore(key, make(chan struct{}, 1))
	ch := v.(chan struct{})
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// pinnedCommit is the base commit of the first catalog.ready fact: re-entries keep the pack they started with.
func pinnedCommit(facts []fact.Fact) string {
	for _, f := range facts {
		if f.Kind == fact.KindCatalogReady {
			if r, err := fact.Decode[fact.CatalogReady](f); err == nil && r.Commit != "" {
				return r.Commit
			}
		}
	}
	return ""
}

// catalogPack returns the context pack for the base commit: cached, else deterministic plus an optional read-only
// Cataloger session. nil means no pack (phase disabled or nothing recognizable); the order proceeds without it.
func (e *Engine) catalogPack(ctx context.Context, o *orch, facts []fact.Fact) (*catalog.Pack, error) {
	if e.Catalog == nil {
		return nil, nil
	}
	commit := pinnedCommit(facts)
	reentry := commit != ""
	if !reentry {
		var err error
		if commit, err = o.git(ctx, o.dir, "rev-parse", o.p.DefaultBranch); err != nil {
			e.log().Warn("catalog: base commit", "wo", o.woID, "err", err)
			return nil, nil
		}
	}
	unlock, err := e.catalogLock(ctx, o.p.Repo+"@"+commit)
	if err != nil {
		return nil, err
	}
	defer unlock()
	pack, ok, err := e.Catalog.Get(ctx, o.p.Repo, commit)
	if err != nil {
		e.log().Warn("catalog: lookup", "wo", o.woID, "err", err)
		return nil, nil
	}
	configured := !reentry && e.roleConfigured(ctx, o.p, harness.RoleCataloger)
	retry := ok && pack.Source == catalog.SourceDeterministic && configured && e.now().Sub(pack.CreatedAt) >= catalogRetryAfter
	if ok && !retry {
		return e.usePack(ctx, o, facts, pack, true)
	}
	if !ok {
		pack = e.deterministicPack(ctx, o, commit)
	}
	pack.Source = catalog.SourceDeterministic
	if configured {
		got, found, err := e.runCataloger(ctx, o, commit)
		switch {
		case err != nil && ctx.Err() != nil:
			return nil, ctx.Err()
		case err != nil:
			e.log().Warn("catalog: cataloger failed", "wo", o.woID, "err", err)
			if _, aerr := e.Facts.Append(ctx, o.woID, fact.KindRunStep, fact.RunStep{Step: "catalog_failed", Summary: firstLine(err.Error(), 200)}); aerr != nil {
				return nil, aerr
			}
		case found:
			pack = catalog.Merge(pack, got)
			pack.Source = catalog.SourceCataloger
		}
	}
	pack.Repo, pack.Commit = o.p.Repo, commit
	pack.CreatedAt = e.now().UTC()
	if err := e.Catalog.Put(ctx, pack); err != nil {
		e.log().Warn("catalog: store", "wo", o.woID, "err", err)
	}
	return e.usePack(ctx, o, facts, pack, false)
}

// usePack treats a pack without files as the "nothing recognizable" marker.
func (e *Engine) usePack(ctx context.Context, o *orch, facts []fact.Fact, pack catalog.Pack, hit bool) (*catalog.Pack, error) {
	if len(pack.Files) == 0 {
		e.log().Warn("catalog: nothing recognizable", "wo", o.woID)
		return nil, nil
	}
	return &pack, e.catalogReady(ctx, o.woID, facts, pack, hit)
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

type treeEntry struct{ mode, path string }

// commitTree lists the blobs of a commit from git, never the worktree; symlinks are kept but flagged by mode.
func (o *orch) commitTree(ctx context.Context, commit string) ([]treeEntry, error) {
	argv := []string{"git", "ls-tree", "-r", "-z", commit}
	out, stderr, code, err := o.sb.Exec(ctx, o.dir, nil, argv...)
	if err != nil || code != 0 {
		return nil, fmt.Errorf("git ls-tree: %v %s", err, firstLine(stderr, 200))
	}
	var tree []treeEntry
	for _, rec := range strings.Split(out, "\x00") {
		meta, p, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		tree = append(tree, treeEntry{mode: strings.Fields(meta)[0], path: p})
	}
	return tree, nil
}

// showBlob reads a regular file at commit through git, skipping symlinks and files over maxManifestBytes.
func (o *orch) showBlob(ctx context.Context, commit, name string) (string, bool) {
	spec := commit + ":" + name
	size, _, code, err := o.sb.Exec(ctx, o.dir, nil, "git", "cat-file", "-s", spec)
	if n, perr := strconv.Atoi(strings.TrimSpace(size)); err != nil || code != 0 || perr != nil || n > maxManifestBytes {
		return "", false
	}
	out, _, code, err := o.sb.Exec(ctx, o.dir, nil, "git", "show", spec)
	return out, err == nil && code == 0
}

// deterministicPack reads manifests from the base commit through git, so nothing in the worktree can influence it.
func (e *Engine) deterministicPack(ctx context.Context, o *orch, commit string) catalog.Pack {
	tree, err := o.commitTree(ctx, commit)
	if err != nil {
		return catalog.Pack{}
	}
	files := map[string]string{}
	var dirs []string
	for _, t := range tree {
		if t.mode == "120000" {
			continue
		}
		if !strings.Contains(t.path, "/") {
			if slices.Contains(manifestNames, t.path) {
				if body, ok := o.showBlob(ctx, commit, t.path); ok {
					files[t.path] = body
				}
			}
		} else if d, _, _ := strings.Cut(t.path, "/"); !strings.HasPrefix(d, ".") && !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	p, err := catalog.Deterministic(files, dirs)
	if err != nil {
		return catalog.Pack{}
	}
	return p
}

var manifestNames = []string{"go.mod", "package.json", "Makefile", "README.md", "README"}

// runCataloger returns found=false when the reply stays unusable after one re-ask; the deterministic pack then stands alone.
func (e *Engine) runCataloger(ctx context.Context, o *orch, commit string) (catalog.Pack, bool, error) {
	o.catalogCommit = commit
	var names []string
	if tree, err := o.commitTree(ctx, commit); err == nil {
		for _, t := range tree {
			names = append(names, t.path)
		}
	}
	list := strings.Join(names, "\n")
	if len(list) > maxTreeBytes {
		cut := maxTreeBytes
		if i := strings.LastIndex(list[:cut], "\n"); i >= 0 {
			cut = i
		}
		list = list[:cut] + "\n…(truncated)"
	}
	n := newNonce()
	prompt := catalog.Prompt(untrustedNote(n, "TREE") + "\n" + fence("TREE", n, list))
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

var (
	readTools = []string{"read", "grep", "glob", "ls", "list", "search", "find", "view", "notebookread"}
	editTools = []string{"edit", "multiedit", "write", "patch", "apply_patch", "notebookedit", "create", "preparing"}
)

// toolKind classifies by the ACP tool kind when the agent sent one, else (claude-agent-acp titles a pending Write "Preparing file…"; opencode uses the bare tool name) a tool call by the first word of its title; shell calls are neither.
func toolKind(ev harness.Event) string {
	switch ev.ToolKind {
	case "read", "search":
		return "read"
	case "edit", "delete", "move":
		return "edit"
	case "execute", "think", "fetch":
		return ""
	}
	title := ev.Tool
	// OpenCode titles some calls "<description>: <tool>"; the tool name is the suffix.
	if i := strings.LastIndex(title, ": "); i >= 0 && !strings.Contains(title[i+2:], " ") {
		title = title[i+2:]
	}
	f := strings.Fields(title)
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

// lockfileManifest maps a lockfile to the manifest (same directory) whose change explains it.
var lockfileManifest = map[string]string{
	"package-lock.json": "package.json", "yarn.lock": "package.json", "pnpm-lock.yaml": "package.json", "npm-shrinkwrap.json": "package.json",
	"go.sum": "go.mod", "Cargo.lock": "Cargo.toml", "poetry.lock": "pyproject.toml", "Pipfile.lock": "Pipfile",
	"composer.lock": "composer.json", "Gemfile.lock": "Gemfile",
}

// lockfileNoise lists changed lockfiles whose manifest did not change alongside, owned or not (lesson #7).
func lockfileNoise(files []string) []string {
	var out []string
	for _, f := range files {
		if m, ok := lockfileManifest[path.Base(f)]; ok && !slices.Contains(files, path.Join(path.Dir(f), m)) {
			out = append(out, f)
		}
	}
	return out
}

func (e *Engine) warnLockfileNoise(ctx context.Context, woID, run string, files []string) error {
	noise := lockfileNoise(files)
	if len(noise) == 0 {
		return nil
	}
	_, err := e.Facts.Append(ctx, woID, fact.KindRunStep, fact.RunStep{Run: run, Step: "lockfile_noise", Summary: "warning: lockfile changes without their manifest: " + strings.Join(noise, ", ")})
	return err
}
