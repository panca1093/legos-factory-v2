package usecase

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"legos-factory/backend/internal/kb"
	"legos-factory/backend/internal/model"
)

const kbListLimit = 500

// KBDocs serves the Knowledge page: browsing both sources and editing,
// approving, discarding or resetting factory documents.
type KBDocs struct {
	KB       *KB
	Projects *Projects
	Proj     ProjectStore
	WO       WorkOrderStore
	DataDir  string
}

type KBListParams struct{ ProjectID, Source, Type, Status, Q string }

type KBDetail struct {
	Doc              model.KBDoc    `json:"doc"`
	Frontmatter      map[string]any `json:"frontmatter"`
	Body             string         `json:"body"`
	Links            []model.KBDoc  `json:"links"`
	Backlinks        []model.KBDoc  `json:"backlinks"`
	Editable         bool           `json:"editable"`
	DefaultAvailable bool           `json:"default_available"`
	Note             string         `json:"note,omitempty"`
}

// List returns factory-wide documents plus those of the requested project, or
// of every project u can reach when none is requested.
func (s *KBDocs) List(ctx context.Context, u *model.User, p KBListParams) ([]model.KBDoc, error) {
	q := model.KBQuery{Limit: kbListLimit}
	if p.Source != "" {
		q.Sources = []string{p.Source}
	}
	if p.Type != "" {
		q.Types = []string{p.Type}
	}
	switch p.Status {
	case "":
	case "all":
		q.Statuses = []string{model.KBStatusDraft, model.KBStatusStable, model.KBStatusDeprecated}
	default:
		q.Statuses = []string{p.Status}
	}
	pids := []string{p.ProjectID}
	if p.ProjectID != "" {
		if err := s.Projects.Authorize(ctx, u, p.ProjectID); err != nil {
			return nil, err
		}
	} else {
		scope, err := s.Projects.Scope(ctx, u)
		if err != nil {
			return nil, err
		}
		if scope == nil {
			all, err := s.Proj.List(ctx)
			if err != nil {
				return nil, err
			}
			for _, pr := range all {
				scope = append(scope, pr.ID)
			}
		}
		pids = append(pids, scope...)
	}
	text := strings.TrimSpace(p.Q)
	seen := map[string]bool{}
	var hits []model.KBHit
	for _, pid := range pids {
		q.ProjectID = pid
		var found []model.KBHit
		if text != "" {
			q.Limit = 50
			var err error
			if found, err = s.KB.Idx.Search(ctx, q, s.KB.Emb.Embed(text)); err != nil {
				return nil, err
			}
		} else {
			docs, err := s.KB.Idx.List(ctx, q)
			if err != nil {
				return nil, err
			}
			for _, d := range docs {
				found = append(found, model.KBHit{Doc: d})
			}
		}
		for _, h := range found {
			if !seen[h.Doc.ID] {
				seen[h.Doc.ID] = true
				hits = append(hits, h)
			}
		}
	}
	sort.SliceStable(hits, func(a, b int) bool {
		x, y := hits[a], hits[b]
		switch {
		case text != "" && x.Score != y.Score:
			return x.Score > y.Score
		case x.Doc.Source != y.Doc.Source && text == "":
			return x.Doc.Source < y.Doc.Source
		case x.Doc.Path != y.Doc.Path:
			return x.Doc.Path < y.Doc.Path
		}
		return x.Doc.ID < y.Doc.ID
	})
	out := make([]model.KBDoc, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.Doc)
	}
	if len(out) > kbListLimit {
		out = out[:kbListLimit]
	}
	return out, nil
}

// Related ranks documents by keyword match alone: factory-wide plus projectID's, stable only.
func (s *KBDocs) Related(ctx context.Context, projectID, text string, limit int) ([]model.KBDoc, error) {
	hits, err := s.KB.Idx.Search(ctx, model.KBQuery{ProjectID: projectID, Text: text, Limit: limit}, nil)
	if err != nil {
		return nil, err
	}
	var out []model.KBDoc
	for _, h := range hits {
		if h.SeeAlso == "" {
			out = append(out, h.Doc)
		}
	}
	return out, nil
}

// doc loads an indexed document u may see; anything else is not found.
func (s *KBDocs) doc(ctx context.Context, u *model.User, id string) (*model.KBDoc, error) {
	d, err := s.KB.Idx.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, model.ErrKBDocNotFound
	}
	if d.ProjectID != "" && s.Projects.Authorize(ctx, u, d.ProjectID) != nil {
		return nil, model.ErrKBDocNotFound
	}
	return d, nil
}

func (s *KBDocs) factoryDoc(ctx context.Context, u *model.User, id string) (*model.KBDoc, error) {
	d, err := s.doc(ctx, u, id)
	if err != nil {
		return nil, err
	}
	if d.Source != model.KBSourceFactory {
		return nil, stateConflict("%s is a repository document; change it through the repository", d.Path)
	}
	return d, nil
}

func (s *KBDocs) Detail(ctx context.Context, u *model.User, id string) (*KBDetail, error) {
	d, err := s.doc(ctx, u, id)
	if err != nil {
		return nil, err
	}
	det := &KBDetail{Doc: *d, Links: []model.KBDoc{}, Backlinks: []model.KBDoc{},
		Editable: d.Source == model.KBSourceFactory && u.Role.CanWrite()}
	if d.Source == model.KBSourceFactory {
		det.DefaultAvailable = s.KB.Bundle.HasDefault(d.Path)
		det.Frontmatter, det.Body, err = s.KB.Bundle.Read(d.Path)
	} else {
		det.Frontmatter, det.Body, det.Note, err = s.readRepoDoc(ctx, d)
	}
	if err != nil {
		return nil, err
	}
	for _, lid := range d.Links {
		if l, err := s.KB.Idx.Get(ctx, lid); err == nil && l != nil {
			det.Links = append(det.Links, *l)
		}
	}
	if back, err := s.KB.Idx.Backlinks(ctx, id); err == nil {
		det.Backlinks = append(det.Backlinks, back...)
	}
	return det, nil
}

// readRepoDoc reads a repo document as committed at the commit it was indexed
// from, in the project's newest work order workspace that has it, else in the
// project's local repository. A document indexed without a commit is read from
// the working tree, never through a symlink.
func (s *KBDocs) readRepoDoc(ctx context.Context, d *model.KBDoc) (map[string]any, string, string, error) {
	var roots []string
	if wos, err := s.WO.List(ctx); err == nil {
		sort.SliceStable(wos, func(a, b int) bool { return wos[a].CreatedAt.After(wos[b].CreatedAt) })
		for _, wo := range wos {
			if wo.ProjectID == d.ProjectID {
				roots = append(roots, filepath.Join(s.DataDir, "workspaces", wo.ID))
			}
		}
	}
	if proj, err := s.Proj.Get(ctx, d.ProjectID); err == nil {
		if info, err := os.Stat(proj.RepoURL); err == nil && info.IsDir() {
			roots = append(roots, proj.RepoURL)
		}
	}
	for _, root := range roots {
		read := func() (map[string]any, string, error) { return kb.ReadMarkdown(root, d.Path) }
		if d.CommitSHA != "" {
			read = func() (map[string]any, string, error) { return kb.ReadGitMarkdown(root, d.CommitSHA, d.Path) }
		}
		if front, body, err := read(); err == nil {
			return front, body, "", nil
		}
	}
	return map[string]any{}, "", "The repository checkout holding this document is not available on this server.", nil
}

func (s *KBDocs) Update(ctx context.Context, u *model.User, id string, front map[string]any, body string) (*model.KBDoc, error) {
	d, err := s.factoryDoc(ctx, u, id)
	if err != nil {
		return nil, err
	}
	if d.ProjectID == "" && !u.Role.IsAdmin() {
		return nil, ErrForbidden
	}
	if t, _ := front["type"].(string); strings.TrimSpace(t) == "" {
		return nil, fmt.Errorf("%w: frontmatter needs a type", ErrInvalid)
	}
	cur, _, err := s.KB.Bundle.Read(d.Path)
	if err != nil {
		return nil, err
	}
	for _, k := range []string{"status", "verified", "generated"} {
		delete(front, k)
		if v, ok := cur[k]; ok {
			front[k] = v
		}
	}
	if err := s.KB.WriteFactory(ctx, d.Path, front, body, "Edit "+d.Path); err != nil {
		return nil, err
	}
	return s.KB.Idx.Get(ctx, id)
}

func (s *KBDocs) draft(ctx context.Context, u *model.User, id string) (*model.KBDoc, error) {
	d, err := s.factoryDoc(ctx, u, id)
	if err != nil {
		return nil, err
	}
	if d.Status != model.KBStatusDraft {
		return nil, stateConflict("%s is not a draft", d.Path)
	}
	return d, nil
}

// Approve turns a draft into a stable document verified by u.
func (s *KBDocs) Approve(ctx context.Context, u *model.User, id, note string) (*model.KBDoc, error) {
	d, err := s.draft(ctx, u, id)
	if err != nil {
		return nil, err
	}
	front, body, err := s.KB.Bundle.Read(d.Path)
	if err != nil {
		return nil, err
	}
	front["status"] = model.KBStatusStable
	front["verified"] = map[string]any{"by": "human:" + u.ID, "at": time.Now().UTC().Format(time.RFC3339)}
	msg := "Approve " + d.Path
	if note = strings.TrimSpace(note); note != "" {
		msg += ": " + note
	}
	if err := s.KB.WriteFactory(ctx, d.Path, front, body, msg); err != nil {
		return nil, err
	}
	return s.KB.Idx.Get(ctx, id)
}

func (s *KBDocs) Discard(ctx context.Context, u *model.User, id string) error {
	d, err := s.draft(ctx, u, id)
	if err != nil {
		return err
	}
	return s.KB.RemoveFactory(ctx, d.Path, "Discard draft "+d.Path)
}

// Reset restores the embedded default of a seeded playbook.
func (s *KBDocs) Reset(ctx context.Context, u *model.User, id string) (*model.KBDoc, error) {
	d, err := s.factoryDoc(ctx, u, id)
	if err != nil {
		return nil, err
	}
	if !s.KB.Bundle.HasDefault(d.Path) {
		return nil, model.ErrKBDocNotFound
	}
	if _, err := s.KB.Bundle.ResetDefault(d.Path, "Reset "+d.Path+" to default"); err != nil {
		return nil, err
	}
	if err := kb.IndexFactoryFile(ctx, s.KB.Idx, s.KB.Emb, s.KB.Bundle, d.Path); err != nil {
		return nil, err
	}
	return s.KB.Idx.Get(ctx, id)
}

// Delete removes a factory doc from bundle and index; seeded defaults are refused since the next start would re-seed them.
func (s *KBDocs) Delete(ctx context.Context, u *model.User, id string) error {
	d, err := s.factoryDoc(ctx, u, id)
	if err != nil {
		return err
	}
	if d.ProjectID == "" && !u.Role.IsAdmin() {
		return ErrForbidden
	}
	if s.KB.Bundle.HasDefault(d.Path) {
		return stateConflict("%s is a default playbook and would be restored on restart; reset it instead", d.Path)
	}
	return s.KB.RemoveFactory(ctx, d.Path, "Delete "+d.Path)
}
