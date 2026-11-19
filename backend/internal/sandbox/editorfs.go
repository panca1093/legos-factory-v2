package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"legos-factory/backend/internal/model"
)

const (
	editorListMax    = 5000
	editorStatusMax  = 20000
	editorGitTimeout = 10 * time.Second
	editorSniffBytes = 8 << 10
	editorMaxLinks   = 16
	editorTmpMarker  = ".legos-tmp-"
)

// EditorFS is the os.Root-confined file layer behind the editor. Content returned by Read is the
// raw file bytes: the BOM, EOL and read-only rules belong to the usecase.
type EditorFS struct{}

// editorBeforeRename lets tests observe the disk between the temp write and the rename.
var editorBeforeRename func()

// checkRel validates a slash-separated path relative to the folder root. allowRoot admits "" and ".".
func checkRel(rel string, allowRoot bool) (string, error) {
	if rel == "" || rel == "." {
		if allowRoot {
			return ".", nil
		}
		return "", fmt.Errorf("%w: empty path", model.ErrBadPath)
	}
	switch {
	case len(rel) > 4096:
		return "", fmt.Errorf("%w: path is too long", model.ErrBadPath)
	case strings.ContainsRune(rel, 0):
		return "", fmt.Errorf("%w: path contains a NUL byte", model.ErrBadPath)
	case strings.Contains(rel, `\`):
		return "", fmt.Errorf("%w: path contains a backslash", model.ErrBadPath)
	case strings.HasPrefix(rel, "/") || !filepath.IsLocal(rel):
		return "", model.ErrOutsideFolder
	}
	rel = path.Clean(rel)
	for _, seg := range strings.Split(rel, "/") {
		if strings.EqualFold(seg, ".git") { // APFS is case-insensitive
			return "", fmt.Errorf("%w: .git is not accessible", model.ErrOutsideFolder)
		}
		if strings.Contains(seg, editorTmpMarker) {
			return "", fmt.Errorf("%w: reserved name", model.ErrBadPath)
		}
	}
	return rel, nil
}

func mapRootErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "path escapes from parent") {
		return model.ErrOutsideFolder
	}
	return err
}

func hashHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func openRoot(root string) (*os.Root, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, model.ErrFolderGone
		}
		return nil, err
	}
	return r, nil
}

func (EditorFS) List(root, rel string) ([]model.FileEntry, int, error) {
	rel, err := checkRel(rel, true)
	if err != nil {
		return nil, 0, err
	}
	r, err := openRoot(root)
	if err != nil {
		return nil, 0, err
	}
	defer r.Close()
	d, err := r.Open(rel)
	if err != nil {
		return nil, 0, mapRootErr(err)
	}
	defer d.Close()
	st, err := d.Stat()
	if err != nil {
		return nil, 0, err
	}
	if !st.IsDir() {
		return nil, 0, fmt.Errorf("%w: not a directory", model.ErrBadPath)
	}
	des, err := d.ReadDir(-1)
	if err != nil {
		return nil, 0, err
	}
	out := make([]model.FileEntry, 0, len(des))
	for _, de := range des {
		name := de.Name()
		if strings.EqualFold(name, ".git") || strings.Contains(name, editorTmpMarker) {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		e := model.FileEntry{Name: name, Kind: "file", Size: info.Size(), MTime: info.ModTime()}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			e.Kind = "symlink"
			_, serr := r.Stat(path.Join(rel, name))
			inside := serr == nil
			e.TargetInside = &inside
		case info.IsDir():
			e.Kind, e.Size = "dir", 0
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		di, dj := out[i].Kind == "dir", out[j].Kind == "dir"
		if di != dj {
			return di
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	truncated := 0
	if len(out) > editorListMax {
		truncated = len(out) - editorListMax
		out = out[:editorListMax]
	}
	return out, truncated, nil
}

// Read returns the file's raw bytes as Content. Files over max are ErrTooLarge, binary ones ErrBinary.
func (EditorFS) Read(root, rel string, max int64) (model.FileContent, error) {
	rel, err := checkRel(rel, false)
	if err != nil {
		return model.FileContent{}, err
	}
	r, err := openRoot(root)
	if err != nil {
		return model.FileContent{}, err
	}
	defer r.Close()
	pre, err := r.Stat(rel)
	if err != nil {
		return model.FileContent{}, mapRootErr(err)
	}
	if pre.IsDir() {
		return model.FileContent{}, fmt.Errorf("%w: %s is a directory", model.ErrBadPath, rel)
	}
	if !pre.Mode().IsRegular() {
		return model.FileContent{}, fmt.Errorf("%w: not a regular file", model.ErrBadPath)
	}
	f, err := r.Open(rel)
	if err != nil {
		return model.FileContent{}, mapRootErr(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return model.FileContent{}, err
	}
	if st.Size() > max {
		return model.FileContent{}, &model.SizedError{Err: model.ErrTooLarge, Size: st.Size()}
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return model.FileContent{}, err
	}
	if int64(len(b)) > max {
		return model.FileContent{}, &model.SizedError{Err: model.ErrTooLarge, Size: int64(len(b))}
	}
	if bytes.IndexByte(b[:min(len(b), editorSniffBytes)], 0) >= 0 {
		return model.FileContent{}, &model.SizedError{Err: model.ErrBinary, Size: int64(len(b))}
	}
	return model.FileContent{Content: string(b), Hash: hashHex(b), MTime: st.ModTime(), Size: int64(len(b))}, nil
}

// resolveLink follows symlinks lexically inside the root so a write lands on the in-root target
// and never replaces the link itself.
func resolveLink(r *os.Root, rel string) (string, error) {
	for range editorMaxLinks {
		st, err := r.Lstat(rel)
		if err != nil || st.Mode()&fs.ModeSymlink == 0 {
			return rel, nil
		}
		target, err := r.Readlink(rel)
		if err != nil {
			return "", mapRootErr(err)
		}
		next := path.Join(path.Dir(rel), filepath.ToSlash(target))
		if strings.HasPrefix(target, "/") || !filepath.IsLocal(next) {
			return "", model.ErrOutsideFolder
		}
		if _, err := checkRel(next, false); err != nil {
			return "", err
		}
		rel = next
	}
	return "", fmt.Errorf("%w: too many levels of symbolic links", model.ErrBadPath)
}

// Write replaces rel atomically (temp file in the same directory, fsync, rename) once baseHash
// matches the disk; create requires that the file does not exist yet.
func (EditorFS) Write(root, rel string, content []byte, baseHash string, create bool) (model.FileStat, error) {
	rel, err := checkRel(rel, false)
	if err != nil {
		return model.FileStat{}, err
	}
	r, err := openRoot(root)
	if err != nil {
		return model.FileStat{}, err
	}
	defer r.Close()
	rel, err = resolveLink(r, rel)
	if err != nil {
		return model.FileStat{}, err
	}
	mode := fs.FileMode(0o644)
	st, serr := r.Stat(rel)
	switch {
	case serr == nil:
		if st.IsDir() || !st.Mode().IsRegular() {
			return model.FileStat{}, fmt.Errorf("%w: not a regular file", model.ErrBadPath)
		}
		b, err := r.ReadFile(rel)
		if err != nil {
			return model.FileStat{}, mapRootErr(err)
		}
		if sum := hashHex(b); create || sum != baseHash {
			return model.FileStat{}, &model.EditorConflict{Current: model.FileStat{Hash: sum, MTime: st.ModTime(), Size: int64(len(b))}}
		}
		mode = st.Mode().Perm()
	case errors.Is(serr, fs.ErrNotExist):
		if !create {
			return model.FileStat{}, serr
		}
	default:
		return model.FileStat{}, mapRootErr(serr)
	}

	dir, name := path.Split(rel)
	tmp := fmt.Sprintf("%s.%s%s%08x", dir, name, editorTmpMarker, rand.Uint32())
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return model.FileStat{}, mapRootErr(err)
	}
	cleanup := func() { _ = r.Remove(tmp) }
	if _, err := f.Write(content); err != nil {
		f.Close()
		cleanup()
		return model.FileStat{}, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		cleanup()
		return model.FileStat{}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return model.FileStat{}, err
	}
	if err := r.Chmod(tmp, mode); err != nil {
		cleanup()
		return model.FileStat{}, err
	}
	if editorBeforeRename != nil {
		editorBeforeRename()
	}
	if create {
		err = r.Link(tmp, rel) // fails when the file appeared meanwhile, unlike Rename
		cleanup()
		if errors.Is(err, fs.ErrExist) {
			return model.FileStat{}, &model.EditorConflict{}
		}
	} else if err = r.Rename(tmp, rel); err != nil {
		cleanup()
	}
	if err != nil {
		return model.FileStat{}, mapRootErr(err)
	}
	ns, err := r.Stat(rel)
	if err != nil {
		return model.FileStat{}, err
	}
	return model.FileStat{Hash: hashHex(content), MTime: ns.ModTime(), Size: int64(len(content))}, nil
}

func (EditorFS) Mkdir(root, rel string) error {
	rel, err := checkRel(rel, false)
	if err != nil {
		return err
	}
	r, err := openRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	if _, err := r.Lstat(rel); err == nil {
		return fs.ErrExist
	}
	return mapRootErr(r.MkdirAll(rel, 0o755))
}

// Rename moves from to to; an existing target is refused. The check and the rename are separate
// steps: a file created at to in between is overwritten.
func (EditorFS) Rename(root, from, to string) error {
	from, err := checkRel(from, false)
	if err != nil {
		return err
	}
	to, err = checkRel(to, false)
	if err != nil {
		return err
	}
	r, err := openRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	if _, err := r.Lstat(from); err != nil {
		return mapRootErr(err)
	}
	if _, err := r.Lstat(to); err == nil && !strings.EqualFold(from, to) { // case-only rename on APFS finds itself
		return fs.ErrExist
	}
	return mapRootErr(r.Rename(from, to))
}

func editorGit(ctx context.Context, root string, stdin []byte, args ...string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, editorGitTimeout)
	defer cancel()
	full := append([]string{"-c", "core.fsmonitor=false", "-c", "core.quotepath=off"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.Bytes(), errb.String(), err
}

func notARepo(stderr string) bool { return strings.Contains(stderr, "not a git repository") }

// Files lists paths for quick open: git ls-files, else a capped walk. Missing and .git paths are dropped.
func (EditorFS) Files(ctx context.Context, root string, max int) ([]string, error) {
	r, err := openRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var paths []string
	out, stderr, gerr := editorGit(ctx, root, nil, "ls-files", "-co", "--exclude-standard", "--deduplicate", "-z")
	switch {
	case gerr == nil:
		for _, p := range strings.Split(string(out), "\x00") {
			if p == "" {
				continue
			}
			if _, err := checkRel(p, false); err != nil {
				continue
			}
			if st, err := r.Lstat(p); err != nil || st.IsDir() {
				continue
			}
			paths = append(paths, p)
		}
	case notARepo(stderr) || errors.Is(gerr, exec.ErrNotFound):
		_ = fs.WalkDir(r.FS(), ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if p != "." && (strings.EqualFold(d.Name(), ".git") || d.Name() == "node_modules") {
					return fs.SkipDir
				}
				return nil
			}
			if _, err := checkRel(p, false); err == nil {
				paths = append(paths, p)
			}
			if len(paths) >= max {
				return fs.SkipAll
			}
			return nil
		})
	default:
		return nil, fmt.Errorf("git ls-files: %w: %s", gerr, strings.TrimSpace(stderr))
	}
	sort.Strings(paths)
	if len(paths) > max {
		paths = paths[:max]
	}
	return paths, nil
}

// GitStatus parses `git status --porcelain=v2 -z --branch`. Paths are relative to root; untracked
// directories keep a trailing slash and X/Y are git's codes ('.' unmodified). A non-repo reports Repo=false.
func (EditorFS) GitStatus(ctx context.Context, root string) (model.GitStatus, error) {
	out, stderr, err := editorGit(ctx, root, nil, "status", "--porcelain=v2", "-z", "--branch", "--untracked-files=normal", "--", ".")
	if err != nil {
		if notARepo(stderr) {
			return model.GitStatus{}, nil
		}
		return model.GitStatus{}, fmt.Errorf("git status: %w: %s", err, strings.TrimSpace(stderr))
	}
	prefix := ""
	if p, _, err := editorGit(ctx, root, nil, "rev-parse", "--show-prefix"); err == nil {
		prefix = strings.TrimSpace(string(p))
	}
	return parseStatus(out, prefix), nil
}

// parseStatus reads porcelain v2 records; prefix is the folder's path inside the repo ("" at the top).
func parseStatus(out []byte, prefix string) model.GitStatus {
	gs := model.GitStatus{Repo: true, Entries: []model.GitStatusEntry{}}
	var oid string
	add := func(p, x, y string) {
		if prefix != "" {
			rest, ok := strings.CutPrefix(p, prefix)
			if !ok || rest == "" {
				return
			}
			p = rest
		}
		gs.Entries = append(gs.Entries, model.GitStatusEntry{Path: p, X: x, Y: y})
	}
	recs := strings.Split(string(out), "\x00")
	for i := 0; i < len(recs); i++ {
		rec := recs[i]
		switch {
		case strings.HasPrefix(rec, "# branch.oid "):
			oid = strings.TrimPrefix(rec, "# branch.oid ")
		case strings.HasPrefix(rec, "# branch.head "):
			gs.Branch = strings.TrimPrefix(rec, "# branch.head ")
		case strings.HasPrefix(rec, "# branch.ab "):
			if f := strings.Fields(strings.TrimPrefix(rec, "# branch.ab ")); len(f) == 2 {
				gs.Ahead, _ = strconv.Atoi(strings.TrimPrefix(f[0], "+"))
				gs.Behind, _ = strconv.Atoi(strings.TrimPrefix(f[1], "-"))
			}
		case strings.HasPrefix(rec, "1 "):
			if f := strings.SplitN(rec, " ", 9); len(f) == 9 && len(f[1]) == 2 {
				add(f[8], f[1][:1], f[1][1:])
			}
		case strings.HasPrefix(rec, "2 "):
			if f := strings.SplitN(rec, " ", 10); len(f) == 10 && len(f[1]) == 2 {
				add(f[9], f[1][:1], f[1][1:])
			}
			i++ // the original path follows as its own record
		case strings.HasPrefix(rec, "u "):
			if f := strings.SplitN(rec, " ", 11); len(f) == 11 && len(f[1]) == 2 {
				add(f[10], f[1][:1], f[1][1:])
			}
		case strings.HasPrefix(rec, "? "):
			add(strings.TrimPrefix(rec, "? "), "?", "?")
		}
	}
	if gs.Branch == "(detached)" {
		gs.Branch = oid[:min(len(oid), 7)]
	}
	if len(gs.Entries) > editorStatusMax {
		gs.Entries, gs.TooMany = nil, true
	}
	return gs
}

// Ignored reports which of rels (relative to root) git ignores; a non-repo ignores nothing.
func (EditorFS) Ignored(ctx context.Context, root string, rels []string) (map[string]bool, error) {
	res := map[string]bool{}
	var in bytes.Buffer
	for _, p := range rels {
		if _, err := checkRel(p, false); err == nil {
			in.WriteString(p)
			in.WriteByte(0)
		}
	}
	if in.Len() == 0 {
		return res, nil
	}
	out, stderr, err := editorGit(ctx, root, in.Bytes(), "check-ignore", "--stdin", "-z")
	if err != nil {
		var ee *exec.ExitError
		if (errors.As(err, &ee) && ee.ExitCode() == 1) || notARepo(stderr) { // 1: none ignored
			return res, nil
		}
		return res, fmt.Errorf("git check-ignore: %w: %s", err, strings.TrimSpace(stderr))
	}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			res[p] = true
		}
	}
	return res, nil
}
