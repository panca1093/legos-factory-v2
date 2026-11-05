package usecase

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"legos-factory/backend/internal/kb"
	"legos-factory/backend/internal/model"
)

var journalID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// JournalStore keeps the work-order journal as Journal Entry documents in the
// factory bundle, read back through the index. It implements KnowledgeStore.
type JournalStore struct {
	KB   *KB
	Proj ProjectStore
}

func journalPath(projectID, id string) string {
	return "projects/" + projectID + "/journal/" + id + ".md"
}

func (j *JournalStore) Insert(ctx context.Context, k *model.KnowledgeRecord) error {
	if k.ProjectID == "" {
		return fmt.Errorf("%w: a journal entry needs a project", ErrInvalid)
	}
	if k.ID == "" {
		k.ID = "kn_" + uuid.NewString()[:12]
	}
	if k.SourceRefs == nil {
		k.SourceRefs = []string{}
	}
	k.CreatedAt = time.Now()
	return j.KB.WriteFactory(ctx, journalPath(k.ProjectID, k.ID), journalFront(k), k.Body,
		"Journal: "+oneLine(k.Kind+" "+k.Title))
}

func journalFront(k *model.KnowledgeRecord) map[string]any {
	at := k.CreatedAt.UTC().Format(time.RFC3339Nano)
	f := map[string]any{
		"type": model.KBTypeJournalEntry, "title": k.Title, "description": firstRunes(oneLine(k.Body), 200),
		"status": model.KBStatusStable, "kind": k.Kind, "actor": k.Actor, "source_refs": k.SourceRefs, "created_at": at,
	}
	for key, v := range map[string]string{"work_order": k.WOID, "commit_sha": k.CommitSHA, "repo_url": k.RepoURL, "workspace_id": k.WorkspaceID} {
		if v != "" {
			f[key] = v
		}
	}
	switch k.Trust {
	case model.TrustVerified:
		f["verified"] = map[string]any{"by": "process:legos-factory", "at": at}
	case model.TrustHumanDecision:
		f["verified"] = map[string]any{"by": "human:" + k.Actor, "at": at}
	default:
		f["generated"] = map[string]any{"by": k.Actor, "at": at}
	}
	return f
}

func firstRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func recordOf(d model.KBDoc, body string) model.KnowledgeRecord {
	m := d.Meta
	r := model.KnowledgeRecord{
		ID: strings.TrimSuffix(path.Base(d.Path), ".md"), WorkspaceID: metaStr(m, "workspace_id"), ProjectID: d.ProjectID,
		WOID: metaStr(m, "work_order"), RepoURL: metaStr(m, "repo_url"), CommitSHA: metaStr(m, "commit_sha"),
		Kind: metaStr(m, "kind"), Trust: model.TrustAIInterpretation, Title: d.Title, Body: body,
		Actor: metaStr(m, "actor"), SourceRefs: []string{}, CreatedAt: metaTime(m["created_at"]),
	}
	if refs, ok := m["source_refs"].([]any); ok {
		for _, x := range refs {
			if s, ok := x.(string); ok {
				r.SourceRefs = append(r.SourceRefs, s)
			}
		}
	}
	if v, ok := m["verified"].(map[string]any); ok {
		r.Trust = model.TrustVerified
		if by, _ := v["by"].(string); strings.HasPrefix(by, "human:") {
			r.Trust = model.TrustHumanDecision
		}
	}
	return r
}

func metaTime(v any) time.Time {
	switch x := v.(type) {
	case time.Time:
		return x
	case string:
		t, _ := time.Parse(time.RFC3339Nano, x)
		return t
	}
	return time.Time{}
}

func (j *JournalStore) Get(ctx context.Context, id string) (*model.KnowledgeRecord, error) {
	if !journalID.MatchString(id) {
		return nil, model.ErrKnowledgeNotFound
	}
	matches, _ := filepath.Glob(filepath.Join(j.KB.Bundle.Root(), "projects", "*", "journal", id+".md"))
	if len(matches) != 1 {
		return nil, model.ErrKnowledgeNotFound
	}
	rel, err := filepath.Rel(j.KB.Bundle.Root(), matches[0])
	if err != nil {
		return nil, err
	}
	rel = filepath.ToSlash(rel)
	d, err := j.KB.Idx.Get(ctx, kb.DocID(model.KBSourceFactory, kb.ProjectOf(rel), rel))
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, model.ErrKnowledgeNotFound
	}
	_, body, err := j.KB.Bundle.Read(rel)
	if err != nil {
		return nil, err
	}
	r := recordOf(*d, body)
	return &r, nil
}

// Search honours the KnowledgeQuery contract of the old table: nil ProjectIDs
// is unrestricted, an empty non-nil slice matches nothing. With Text the
// index ranks; otherwise newest first.
func (j *JournalStore) Search(ctx context.Context, q model.KnowledgeQuery) ([]model.KnowledgeRecord, error) {
	pids := q.ProjectIDs
	if pids == nil {
		all, err := j.Proj.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range all {
			pids = append(pids, p.ID)
		}
	}
	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	text := strings.TrimSpace(q.Text)
	var hits []model.KBHit
	for _, pid := range pids {
		kq := model.KBQuery{ProjectID: pid, Sources: []string{model.KBSourceFactory}, Types: []string{model.KBTypeJournalEntry}, Limit: 100000}
		if text != "" {
			kq.Limit = 200
			found, err := j.KB.Idx.Search(ctx, kq, j.KB.Emb.Embed(text))
			if err != nil {
				return nil, err
			}
			hits = append(hits, found...)
			continue
		}
		docs, err := j.KB.Idx.List(ctx, kq)
		if err != nil {
			return nil, err
		}
		for _, d := range docs {
			hits = append(hits, model.KBHit{Doc: d})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if text != "" {
			return hits[a].Score > hits[b].Score
		}
		return metaTime(hits[a].Doc.Meta["created_at"]).After(metaTime(hits[b].Doc.Meta["created_at"]))
	})
	out := []model.KnowledgeRecord{}
	for _, h := range hits {
		d := h.Doc
		if d.Type != model.KBTypeJournalEntry || d.ProjectID == "" {
			continue
		}
		r := recordOf(d, "")
		if (q.WOID != "" && r.WOID != q.WOID) || (q.Trust != "" && r.Trust != q.Trust) {
			continue
		}
		_, body, err := j.KB.Bundle.Read(d.Path)
		if err != nil {
			continue
		}
		r.Body = body
		out = append(out, r)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// LegacyRecords is the old knowledge_records table, read once for the export.
type LegacyRecords interface {
	All(ctx context.Context) ([]model.KnowledgeRecord, error)
}

// exportDone is written last, so a crash mid-export leaves the export to be
// redone; rewriting a record with the same content is harmless.
const exportDone = "journal-export.done"

// Export writes the legacy rows into the bundle in one commit and indexes
// them, until the exportDone marker says it completed.
func (j *JournalStore) Export(ctx context.Context, src LegacyRecords) (int, error) {
	marker := filepath.Join(j.KB.Bundle.Root(), exportDone)
	if _, err := os.Stat(marker); err == nil {
		return 0, nil
	}
	rows, err := src.All(ctx)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	files := make([]kb.File, 0, len(rows))
	for i := range rows {
		k := &rows[i]
		files = append(files, kb.File{Path: journalPath(k.ProjectID, k.ID), Front: journalFront(k), Body: k.Body})
	}
	if _, err := j.KB.Bundle.WriteMany(files, fmt.Sprintf("Export %d knowledge records", len(rows))); err != nil {
		return 0, err
	}
	if _, err := j.KB.SyncFactory(ctx); err != nil {
		return len(rows), err
	}
	return len(rows), os.WriteFile(marker, []byte("done\n"), 0o644)
}
