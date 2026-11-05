package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"legos-factory/backend/internal/kb"
	"legos-factory/backend/internal/model"
)

const (
	kbPointerLimit   = 8
	maxPlaybookChars = 8000
	kbPrecedence     = "If a repository document and a factory playbook disagree, follow the repository. If you cannot tell which applies, ask a human."
)

// KB ties the factory bundle, the index and the embedder together: factory
// writes are indexed immediately, repository syncs are serialised per project.
type KB struct {
	Bundle *kb.Bundle
	Idx    kb.Index
	Emb    kb.Embedder
	Log    *slog.Logger

	mu     sync.Mutex
	locks  map[string]*sync.Mutex
	synced map[string]string // project → repository commit last indexed
	dirs   map[string]string // project → checkout last indexed, to read repo playbooks at their commit
}

func (k *KB) projectLock(pid string) *sync.Mutex {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.locks == nil {
		k.locks, k.synced = map[string]*sync.Mutex{}, map[string]string{}
	}
	if k.locks[pid] == nil {
		k.locks[pid] = &sync.Mutex{}
	}
	return k.locks[pid]
}

func (k *KB) repoCommit(pid string) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.synced[pid]
}

func (k *KB) WriteFactory(ctx context.Context, rel string, front map[string]any, body, message string) error {
	if _, err := k.Bundle.Write(rel, front, body, message); err != nil {
		return err
	}
	return kb.IndexFactoryFile(ctx, k.Idx, k.Emb, k.Bundle, rel)
}

func (k *KB) RemoveFactory(ctx context.Context, rel, message string) error {
	if _, err := k.Bundle.Remove(rel, message); err != nil {
		return err
	}
	return k.Idx.Delete(ctx, kb.DocID(model.KBSourceFactory, kb.ProjectOf(rel), rel))
}

func (k *KB) SyncFactory(ctx context.Context) (kb.SyncStats, error) {
	return kb.SyncFactory(ctx, k.Idx, k.Emb, k.Bundle)
}

// SyncRepo indexes the repository checkout in dir for a project. Unless force
// is set it does nothing when the checkout's HEAD is the commit last indexed.
func (k *KB) SyncRepo(ctx context.Context, projectID, dir string, force bool) (st kb.SyncStats, sha string, skipped bool, err error) {
	l := k.projectLock(projectID)
	l.Lock()
	defer l.Unlock()
	sha = kb.GitHead(dir)
	k.mu.Lock()
	last, seen := k.synced[projectID]
	k.mu.Unlock()
	if !force && seen && last == sha {
		return st, sha, true, nil
	}
	k.mu.Lock()
	if k.dirs == nil {
		k.dirs = map[string]string{}
	}
	k.dirs[projectID] = dir
	k.mu.Unlock()
	if st, err = kb.Sync(ctx, k.Idx, k.Emb, model.KBSourceRepo, projectID, dir, sha); err != nil {
		return st, sha, false, err
	}
	k.mu.Lock()
	k.synced[projectID] = sha
	k.mu.Unlock()
	return st, sha, false, nil
}

// Playbooks returns the stable playbooks written for role, repository ones
// first since the repo wins, each ordered by path. A playbook without a
// specialty applies to every specialty of its role.
func (k *KB) Playbooks(ctx context.Context, projectID, role, specialty string) []model.KBDoc {
	docs, err := k.Idx.List(ctx, model.KBQuery{
		ProjectID: projectID, Sources: []string{model.KBSourceRepo, model.KBSourceFactory}, Types: []string{model.KBTypePlaybook}, Limit: 500,
	})
	if err != nil {
		k.Log.Warn("kb: playbook lookup failed", "err", err)
		return nil
	}
	var repo, factory []model.KBDoc
	for _, d := range docs {
		if metaStr(d.Meta, "role") != role {
			continue
		}
		if s := metaStr(d.Meta, "specialty"); s != "" && s != specialty {
			continue
		}
		if d.Source == model.KBSourceRepo {
			repo = append(repo, d)
		} else {
			factory = append(factory, d)
		}
	}
	return append(repo, factory...)
}

func (k *KB) playbookBody(d model.KBDoc) (string, error) {
	if d.Source == model.KBSourceFactory {
		_, body, err := k.Bundle.Read(d.Path)
		return body, err
	}
	k.mu.Lock()
	dir := k.dirs[d.ProjectID]
	k.mu.Unlock()
	if dir == "" {
		return "", fmt.Errorf("no checkout indexed for project %s", d.ProjectID)
	}
	if d.CommitSHA == "" {
		_, body, err := kb.ReadMarkdown(dir, d.Path)
		return body, err
	}
	_, body, err := kb.ReadGitMarkdown(dir, d.CommitSHA, d.Path)
	return body, err
}

// Context builds the knowledge block appended to an agent prompt: matching
// playbooks inlined, repository documents as pointers, then the precedence
// rule. The audit map names the commits and documents the run saw.
func (k *KB) Context(ctx context.Context, projectID, role, specialty, query string) (string, map[string]any) {
	if k == nil {
		return "", nil
	}
	var b strings.Builder
	ids := []string{}
	pbs := k.Playbooks(ctx, projectID, role, specialty)
	heading := ""
	for _, d := range pbs {
		body, err := k.playbookBody(d)
		if err != nil {
			k.Log.Warn("kb: playbook unreadable", "doc", d.ID, "err", err)
			continue
		}
		if h := playbookHeading(d.Source); h != heading {
			heading = h
			b.WriteString("\n\n" + h + "\n")
		}
		ids = append(ids, d.ID)
		fmt.Fprintf(&b, "\n--- %s ---\n%s\n", d.Title, truncate(strings.TrimSpace(body), maxPlaybookChars))
	}
	hits, err := k.Idx.Search(ctx, model.KBQuery{
		ProjectID: projectID, Text: query, Sources: []string{model.KBSourceRepo}, Limit: kbPointerLimit,
	}, k.Emb.Embed(query))
	if err != nil {
		k.Log.Warn("kb: repository lookup failed", "err", err)
	}
	// playbooks reach agents by role above; as pointers they would repeat or leak other roles' rules
	hits = slices.DeleteFunc(hits, func(h model.KBHit) bool { return h.Doc.Type == model.KBTypePlaybook })
	if len(hits) > 0 {
		b.WriteString("\n\nRepository documents that may matter for this work. They are in your workspace; read the ones you need:\n")
		for _, h := range hits {
			ids = append(ids, h.Doc.ID)
			fmt.Fprintf(&b, "- %s — %s — %s", h.Doc.Path, h.Doc.Title, oneLine(h.Doc.Description))
			if h.SeeAlso != "" {
				fmt.Fprintf(&b, " (see also, linked from %s)", docPath(h.SeeAlso))
			}
			b.WriteString("\n")
		}
	}
	if len(pbs) > 0 || len(hits) > 0 {
		b.WriteString("\n" + kbPrecedence + "\n")
	}
	return b.String(), map[string]any{
		"kb_repo_commit": k.repoCommit(projectID), "kb_factory_commit": k.Bundle.Head(), "kb_docs": ids,
	}
}

func playbookHeading(source string) string {
	if source == model.KBSourceRepo {
		return "Repository playbooks for your role (from this repo; they win over factory playbooks):"
	}
	return "Factory playbooks for your role:"
}

func docPath(id string) string {
	if parts := strings.SplitN(id, ":", 3); len(parts) == 3 {
		return parts[2]
	}
	return id
}

func metaStr(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}
