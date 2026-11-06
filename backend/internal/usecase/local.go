package usecase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"legos-factory/backend/internal/model"
)

// LocalHost is the machine the backend runs on: its folders and the user's
// own git. Only used in dev auth mode, where backend and user share a laptop.
type LocalHost interface {
	IsGitRepo(dir string) bool
	OriginURL(ctx context.Context, dir string) string
	DefaultBranch(ctx context.Context, dir string) string
	Clone(ctx context.Context, url, target string) error
	Init(ctx context.Context, dir string) error
	PickFolder(ctx context.Context) (string, error)
}

// Local backs the /local endpoints that add a project from a folder.
type Local struct {
	Host LocalHost
	GH   func(repo string) GitProvider
}

var githubRemote = regexp.MustCompile(`(?i)^(?:https?://(?:[^@/]+@)?|ssh://(?:[^@/]+@)?|git://|[^@/\s]+@)github\.com[:/]([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

// ParseGitHubRepo returns "owner/repo" for a github.com https, ssh or scp-style
// remote URL, else "".
func ParseGitHubRepo(remote string) string {
	m := githubRemote.FindStringSubmatch(strings.TrimSpace(remote))
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}

func expandPath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func absDir(p string) (string, error) {
	p = expandPath(strings.TrimSpace(p))
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%w: path must be absolute", ErrInvalid)
	}
	p = filepath.Clean(p)
	if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%w: %s is not an existing folder", ErrInvalid, p)
	}
	return p, nil
}

func (l *Local) PickFolder(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	return l.Host.PickFolder(ctx)
}

// Inspect describes path; when it is not a git repo, its direct child repos
// are listed in Repos.
func (l *Local) Inspect(ctx context.Context, path string) (*model.LocalRepo, error) {
	dir, err := absDir(path)
	if err != nil {
		return nil, err
	}
	r := l.describe(ctx, dir)
	if r.IsGit {
		return &r, nil
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if len(r.Repos) >= 200 {
			break
		}
		child := filepath.Join(dir, e.Name())
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && l.Host.IsGitRepo(child) {
			r.Repos = append(r.Repos, l.describe(ctx, child))
		}
	}
	sort.Slice(r.Repos, func(i, j int) bool { return r.Repos[i].Name < r.Repos[j].Name })
	return &r, nil
}

func (l *Local) describe(ctx context.Context, dir string) model.LocalRepo {
	r := model.LocalRepo{Path: dir, Name: filepath.Base(dir), Repos: []model.LocalRepo{}, Language: guessLanguage(dir)}
	if r.IsGit = l.Host.IsGitRepo(dir); r.IsGit {
		r.GitHubRepo = ParseGitHubRepo(l.Host.OriginURL(ctx, dir))
		r.DefaultBranch = l.Host.DefaultBranch(ctx, dir)
	}
	return r
}

func guessLanguage(dir string) string {
	if lang := detectProfile(dir).Language; lang != "" {
		return lang
	}
	for _, m := range []string{"pyproject.toml", "requirements.txt"} {
		if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
			return "python"
		}
	}
	return ""
}

var cloneURL = regexp.MustCompile(`^(?:(?:https?|ssh|git)://[^\s]+|[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^\s]+)$`)

// Clone runs the user's own git into parentDir/<repo-name>.
func (l *Local) Clone(ctx context.Context, url, parentDir string) (*model.LocalRepo, error) {
	url = strings.TrimSpace(url)
	if !cloneURL.MatchString(url) {
		return nil, fmt.Errorf("%w: url must be an https, ssh, git or user@host:path git URL", ErrInvalid)
	}
	parent, err := absDir(parentDir)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(strings.TrimSuffix(strings.TrimRight(strings.ReplaceAll(url, ":", "/"), "/"), ".git"))
	if name == "" || name == "." || name == ".." {
		return nil, fmt.Errorf("%w: cannot derive a folder name from the url", ErrInvalid)
	}
	target := filepath.Join(parent, name)
	if _, err := os.Lstat(target); err == nil {
		return nil, fmt.Errorf("%w: %s already exists", ErrInvalid, target)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if err := l.Host.Clone(ctx, url, target); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return l.Inspect(ctx, target)
}

// Init creates parentDir/name as a new git repository.
func (l *Local) Init(ctx context.Context, name, parentDir string) (*model.LocalRepo, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, "-") || strings.ContainsAny(name, `/\`) {
		return nil, fmt.Errorf("%w: name must be a plain folder name", ErrInvalid)
	}
	parent, err := absDir(parentDir)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(parent, name)
	if _, err := os.Lstat(target); err == nil {
		return nil, fmt.Errorf("%w: %s already exists", ErrInvalid, target)
	}
	if err := l.Host.Init(ctx, target); err != nil {
		return nil, err
	}
	return l.Inspect(ctx, target)
}

// GHStatus reports whether the gh CLI is installed and logged in.
func (l *Local) GHStatus(ctx context.Context) (installed, authenticated bool, account string) {
	if l.GH == nil {
		return false, false, ""
	}
	account, err := l.GH("").CheckAuth(ctx)
	switch {
	case err == nil:
		return true, true, account
	case errors.Is(err, model.ErrGHMissing):
		return false, false, ""
	}
	return true, false, ""
}
