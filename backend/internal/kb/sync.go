package kb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"legos-factory/backend/internal/model"
)

const (
	maxDocBytes = 256 << 10
	maxDocs     = 2000
)

var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true}

type SyncStats struct{ Docs, Changed, Removed int }

type file struct {
	rel     string
	content []byte
}

// Sync brings the index in line with the markdown files under root.
func Sync(ctx context.Context, idx Index, emb Embedder, source, projectID, root, commitSHA string) (SyncStats, error) {
	files, err := collect(root, source == model.KBSourceFactory)
	if err != nil {
		return SyncStats{}, err
	}
	return syncFiles(ctx, idx, emb, source, projectID, files, commitSHA)
}

func syncFiles(ctx context.Context, idx Index, emb Embedder, source, projectID string, files []file, commitSHA string) (SyncStats, error) {
	var st SyncStats
	byName := map[string]string{}
	for _, f := range files {
		k := strings.ToLower(strings.TrimSuffix(path.Base(f.rel), ".md"))
		if _, ok := byName[k]; !ok {
			byName[k] = f.rel
		}
	}
	resolve := func(n string) (string, bool) {
		p, ok := byName[strings.ToLower(n)]
		return p, ok
	}
	have, err := idx.Hashes(ctx, source, projectID)
	if err != nil {
		return st, err
	}
	st.Docs = len(files)
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		seen[f.rel] = true
		sum := sha256.Sum256(f.content)
		if have[f.rel] == hex.EncodeToString(sum[:]) {
			if err := idx.Touch(ctx, DocID(source, projectID, f.rel), commitSHA); err != nil {
				return st, err
			}
			continue
		}
		p, err := Parse(source, projectID, f.rel, f.content, resolve)
		if err != nil {
			continue
		}
		p.CommitSHA = commitSHA
		if err := idx.Upsert(ctx, p, embedText(emb, p)); err != nil {
			return st, err
		}
		st.Changed++
	}
	for p := range have {
		if seen[p] {
			continue
		}
		if err := idx.Delete(ctx, DocID(source, projectID, p)); err != nil {
			return st, err
		}
		st.Removed++
	}
	return st, nil
}

func collect(root string, factory bool) ([]file, error) {
	paths, err := listMD(root, factory)
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var out []file
	for _, rel := range paths {
		if len(out) >= maxDocs && !factory {
			break
		}
		if skipped(rel, factory) {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if info, err := os.Lstat(abs); err != nil || !info.Mode().IsRegular() || info.Size() > maxDocBytes {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		out = append(out, file{rel, data})
	}
	return out, nil
}

func skipped(rel string, factory bool) bool {
	parts := strings.Split(rel, "/")
	for _, d := range parts[:len(parts)-1] {
		if skipDirs[d] {
			return true
		}
	}
	base := parts[len(parts)-1]
	return !strings.HasSuffix(base, ".md") || (factory && (base == "index.md" || base == "log.md"))
}

// listMD lists markdown files; a repository source indexes tracked files only,
// so nothing an agent wrote into the workspace reaches the index.
func listMD(root string, factory bool) ([]string, error) {
	args := []string{"-C", root, "ls-files", "-z", "*.md"}
	if factory {
		args = []string{"-C", root, "ls-files", "-co", "--exclude-standard", "-z", "*.md"}
	}
	cmd := exec.Command("git", args...)
	if out, err := cmd.Output(); err == nil && isWorkTree(root) {
		seen := map[string]bool{}
		var paths []string
		for _, p := range bytes.Split(out, []byte{0}) {
			if s := string(p); s != "" && !seen[s] {
				seen[s] = true
				paths = append(paths, s)
			}
		}
		return paths, nil
	}
	var paths []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != root && skipDirs[d.Name()] {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	return paths, err
}

func isWorkTree(root string) bool {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--is-inside-work-tree").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func embedText(emb Embedder, p model.KBParsed) []float32 {
	return emb.Embed(p.Title + "\n" + p.Description + "\n" + p.Headings + "\n" + p.Body)
}
