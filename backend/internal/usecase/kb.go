package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"sync"

	"legos-factory/backend/internal/kb"
	"legos-factory/backend/internal/model"
)

const (
	maxPlaybookChars = 8000
	kbPrecedence     = "If a repository document and a factory playbook disagree, follow the repository. If you cannot tell which applies, ask a human."
)

// KB ties the factory bundle, the index and the embedder together: factory
// writes are indexed immediately.
type KB struct {
	Bundle *kb.Bundle
	Idx    kb.Index
	Emb    kb.Embedder
	Log    *slog.Logger
	// Projects lists the live projects; factory folders of other projects are not indexed. Nil indexes all.
	Projects interface {
		List(ctx context.Context) ([]model.Project, error)
	}

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (k *KB) projectLock(pid string) *sync.Mutex {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.locks == nil {
		k.locks = map[string]*sync.Mutex{}
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
	var live func(string) bool
	if k.Projects != nil {
		if ps, err := k.Projects.List(ctx); err != nil {
			if k.Log != nil {
				k.Log.Warn("listing projects for knowledge sync failed; indexing every project folder", "err", err)
			}
		} else {
			ids := make(map[string]bool, len(ps))
			for _, p := range ps {
				ids[p.ID] = true
			}
			live = func(id string) bool { return ids[id] }
		}
	}
	st, err := kb.SyncFactory(ctx, k.Idx, k.Emb, k.Bundle, live)
	if len(st.Skipped) > 0 && k.Log != nil {
		k.Log.Warn("factory knowledge of deleted projects not indexed", "projects", st.Skipped)
	}
	return st, err
}

// RemoveProject deletes a deleted project's factory knowledge folder; its index rows go with the project.
func (k *KB) RemoveProject(projectID string) error {
	_, err := k.Bundle.RemoveProject(projectID, "Remove knowledge of deleted project "+projectID)
	return err
}

// Playbooks returns the stable playbooks written for role, repository ones
// first since the repo wins, each ordered by path. A playbook without a
// specialty applies to every specialty of its role. Repository playbooks are
// read from git at baseSHA; without a base there are none.
func (k *KB) Playbooks(ctx context.Context, projectID, role, specialty, dir, baseSHA string) []model.KBDoc {
	var out []model.KBDoc
	match := func(meta map[string]any) bool {
		if r := metaStr(meta, "role"); r != role && r != playbookAllRoles {
			return false
		}
		s := metaStr(meta, "specialty")
		return s == "" || s == specialty
	}
	if baseSHA != "" {
		out = k.gitPlaybooks(projectID, dir, baseSHA, match)
	}
	docs, err := k.Idx.List(ctx, model.KBQuery{
		ProjectID: projectID, Sources: []string{model.KBSourceFactory}, Types: []string{model.KBTypePlaybook}, Limit: 500,
	})
	if err != nil {
		k.Log.Warn("kb: playbook lookup failed", "source", model.KBSourceFactory, "err", err)
	}
	for _, d := range docs {
		if match(d.Meta) {
			out = append(out, d)
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
		if st := metaStr(front, "status"); (st != "" && st != model.KBStatusStable) || !match(front) {
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
// never from another work order's checkout.
func (k *KB) playbookBody(d model.KBDoc, dir, baseSHA string) (string, error) {
	if d.Source == model.KBSourceFactory {
		_, body, err := k.Bundle.Read(d.Path)
		return body, err
	}
	_, body, err := kb.ReadGitMarkdown(dir, baseSHA, d.Path)
	return body, err
}

// Context builds the knowledge block appended to an agent prompt: matching
// playbooks inlined, then the precedence
// rule. dir and baseSHA are the work order's workspace and planning base; they
// decide which repo playbook text is read. The audit map names the commits and
// documents the run saw.
func (k *KB) Context(ctx context.Context, projectID, role, specialty, dir, baseSHA string) (string, map[string]any) {
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
	if len(inlined) > 0 {
		b.WriteString("\n" + kbPrecedence + "\n")
	}
	return b.String(), map[string]any{
		"kb_repo_commit": baseSHA, "kb_factory_commit": k.Bundle.Head(), "kb_docs": ids,
	}
}

// playbookAllRoles is the role value of a playbook every agent role receives.
const playbookAllRoles = "all"

func playbookHeading(source string) string {
	if source == model.KBSourceRepo {
		return "Repository playbooks for your role (from this repo; they win over factory playbooks):"
	}
	return "Factory playbooks for your role:"
}

func metaStr(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}
