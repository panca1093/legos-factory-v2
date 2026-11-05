package kb

import (
	"context"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"legos-factory/backend/internal/model"
)

// ProjectOf maps a factory path to its project: projects/<pid>/... → pid, anything else "".
func ProjectOf(rel string) string {
	if rest, ok := strings.CutPrefix(rel, "projects/"); ok {
		if pid, _, ok := strings.Cut(rest, "/"); ok {
			return pid
		}
	}
	return ""
}

// SyncFactory indexes the whole bundle, one project at a time. A project that
// fails (for example one the database does not know) does not stop the others.
func SyncFactory(ctx context.Context, idx Index, emb Embedder, b *Bundle) (SyncStats, error) {
	files, err := collect(b.root, true)
	if err != nil {
		return SyncStats{}, err
	}
	groups := map[string][]file{"": nil}
	for _, f := range files {
		pid := ProjectOf(f.rel)
		groups[pid] = append(groups[pid], f)
	}
	pids := make([]string, 0, len(groups))
	for pid := range groups {
		pids = append(pids, pid)
	}
	sort.Strings(pids)
	var total SyncStats
	var first error
	commit := b.Head()
	for _, pid := range pids {
		st, err := syncFiles(ctx, idx, emb, model.KBSourceFactory, pid, groups[pid], commit)
		total.Docs += st.Docs
		total.Changed += st.Changed
		total.Removed += st.Removed
		if err != nil && first == nil {
			first = err
		}
	}
	return total, first
}

// IndexFactoryFile re-indexes one bundle file after a write; OKF reserved files are skipped.
func IndexFactoryFile(ctx context.Context, idx Index, emb Embedder, b *Bundle, rel string) error {
	rel = path.Clean(filepath.ToSlash(rel))
	if base := path.Base(rel); base == "index.md" || base == "log.md" {
		return nil
	}
	data, err := readInRoot(b.root, rel)
	if err != nil {
		return err
	}
	p, err := Parse(model.KBSourceFactory, ProjectOf(rel), rel, data, nil)
	if err != nil {
		return err
	}
	p.CommitSHA = b.Head()
	return idx.Upsert(ctx, p, embedText(emb, p))
}

// GitHead returns the HEAD commit of dir, or "" when dir is not a git repository.
func GitHead(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
