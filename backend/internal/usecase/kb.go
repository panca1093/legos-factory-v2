package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"path"
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
// specialty applies to every specialty of its role. Repository playbooks are
// discovered at baseSHA, never from the shared index another work order may
// have replaced; without a base only a non-git workspace falls back to the index.
func (k *KB) Playbooks(ctx context.Context, projectID, role, specialty, dir, baseSHA string) []model.KBDoc {
	var out []model.KBDoc
	match := func(meta map[string]any) bool {
		if metaStr(meta, "role") != role {
			return false
		}
		s := metaStr(meta, "specialty")
		return s == "" || s == specialty
	}
	if baseSHA != "" {
		out = k.gitPlaybooks(projectID, dir, baseSHA, match)
	}
	for _, source := range []string{model.KBSourceRepo, model.KBSourceFactory} {
		if source == model.KBSourceRepo && (baseSHA != "" || dir == "" || kb.GitHead(dir) != "") {
			continue
		}
		docs, err := k.Idx.List(ctx, model.KBQuery{
			ProjectID: projectID, Sources: []string{source}, Types: []string{model.KBTypePlaybook}, Limit: 500,
		})
		if err != nil {
			k.Log.Warn("kb: playbook lookup failed", "source", source, "err", err)
			continue
		}
		for _, d := range docs {
			if match(d.Meta) {
				out = append(out, d)
			}
		}
	}
	return out
}

func (k *KB) gitPlaybooks(projectID, dir, baseSHA string, match func(map[string]any) bool) []model.KBDoc {
	paths, err := kb.GitPlaybookPaths(dir, baseSHA)
	if err != nil {
		k.Log.Warn("kb: playbook lookup failed", "source", model.KBSourceRepo, "err", err)
		return nil
	}
	var out []model.KBDoc
	for _, p := range paths {
		front, _, err := kb.ReadGitMarkdown(dir, baseSHA, p)
		if err != nil || metaStr(front, "type") != model.KBTypePlaybook {
			continue
		}
		if st := metaStr(front, "status"); st == model.KBStatusDraft || st == model.KBStatusDeprecated || !match(front) {
			continue
		}
		title := metaStr(front, "title")
		if title == "" {
			title = strings.TrimSuffix(path.Base(p), ".md")
		}
		out = append(out, model.KBDoc{
			ID: kb.DocID(model.KBSourceRepo, projectID, p), Source: model.KBSourceRepo, ProjectID: projectID,
			Path: p, Type: model.KBTypePlaybook, Title: title, Meta: front,
		})
	}
	return out
}

// playbookBody reads a repo playbook at the work order's own planning base,
// never from another work order's checkout. Without a base it falls back to
// the workspace files only when that is not a git repository.
func (k *KB) playbookBody(d model.KBDoc, dir, baseSHA string) (string, error) {
	if d.Source == model.KBSourceFactory {
		_, body, err := k.Bundle.Read(d.Path)
		return body, err
	}
	if baseSHA != "" {
		_, body, err := kb.ReadGitMarkdown(dir, baseSHA, d.Path)
		return body, err
	}
	if dir == "" || kb.GitHead(dir) != "" {
		return "", fmt.Errorf("no planning base recorded for %s", d.ID)
	}
	_, body, err := kb.ReadMarkdown(dir, d.Path)
	return body, err
}

// Context builds the knowledge block appended to an agent prompt: matching
// playbooks inlined, repository documents as pointers, then the precedence
// rule. dir and baseSHA are the work order's workspace and planning base; they
// decide which repo playbook text is read. The audit map names the commits and
// documents the run saw.
func (k *KB) Context(ctx context.Context, projectID, role, specialty, query, dir, baseSHA string) (string, map[string]any) {
	if k == nil {
		return "", nil
	}
	var b strings.Builder
	ids := []string{}
	inlined := map[string]bool{}
	heading := ""
	for _, d := range k.Playbooks(ctx, projectID, role, specialty, dir, baseSHA) {
		body, err := k.playbookBody(d, dir, baseSHA)
		if err != nil {
			k.Log.Warn("kb: playbook unreadable", "doc", d.ID, "err", err)
			continue
		}
		if h := playbookHeading(d.Source); h != heading {
			heading = h
			b.WriteString("\n\n" + h + "\n")
		}
		ids = append(ids, d.ID)
		inlined[d.ID] = true
		fmt.Fprintf(&b, "\n--- %s ---\n%s\n", d.Title, truncate(strings.TrimSpace(body), maxPlaybookChars))
	}
	hits, err := k.Idx.Search(ctx, model.KBQuery{
		ProjectID: projectID, Text: query, Sources: []string{model.KBSourceRepo}, Limit: kbPointerLimit,
	}, k.Emb.Embed(query))
	if err != nil {
		k.Log.Warn("kb: repository lookup failed", "err", err)
	}
	// a playbook is delivered inline by role; as a pointer it would repeat or leak another role's rules
	hits = slices.DeleteFunc(hits, func(h model.KBHit) bool {
		return inlined[h.Doc.ID] || h.Doc.Type == model.KBTypePlaybook && agentRoles[metaStr(h.Doc.Meta, "role")]
	})
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
	if len(inlined) > 0 || len(hits) > 0 {
		b.WriteString("\n" + kbPrecedence + "\n")
	}
	return b.String(), map[string]any{
		"kb_repo_commit": baseSHA, "kb_factory_commit": k.Bundle.Head(), "kb_docs": ids,
	}
}

var agentRoles = map[string]bool{
	string(model.AgentArchitect): true, string(model.AgentForeman): true,
	string(model.AgentBuilder): true, string(model.AgentInspector): true,
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
