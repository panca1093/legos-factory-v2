package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"legos-factory/backend/internal/model"
)

type KnowledgeRepo struct{ DB *sql.DB }

const kCols = `k.id, k.workspace_id, k.project_id, k.wo_id, k.repo_url, k.commit_sha, k.kind, k.trust,
	k.title, k.body, k.actor, k.source_refs, k.created_at`

func (r *KnowledgeRepo) Insert(ctx context.Context, k *model.KnowledgeRecord) error {
	if k.ID == "" {
		k.ID = "kn_" + uuid.NewString()[:12]
	}
	if k.SourceRefs == nil {
		k.SourceRefs = []string{}
	}
	k.CreatedAt = time.Now()
	refs, _ := json.Marshal(k.SourceRefs)
	_, err := r.DB.ExecContext(ctx, `INSERT INTO knowledge_records
		(id, workspace_id, project_id, wo_id, repo_url, commit_sha, kind, trust, title, body, actor, source_refs, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		k.ID, k.WorkspaceID, k.ProjectID, nullStr(k.WOID), k.RepoURL, k.CommitSHA, k.Kind, string(k.Trust),
		k.Title, k.Body, k.Actor, string(refs), k.CreatedAt.UnixNano())
	return err
}

func (r *KnowledgeRepo) Get(ctx context.Context, id string) (*model.KnowledgeRecord, error) {
	row := r.DB.QueryRowContext(ctx, `SELECT `+kCols+` FROM knowledge_records k WHERE k.id = ?`, id)
	k, err := scanK(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrKnowledgeNotFound
	}
	return k, err
}

// Search returns records matching q. With Text it ranks by FTS5 bm25 over
// title/body; otherwise newest first.
func (r *KnowledgeRepo) Search(ctx context.Context, q model.KnowledgeQuery) ([]model.KnowledgeRecord, error) {
	if q.ProjectIDs != nil && len(q.ProjectIDs) == 0 {
		return []model.KnowledgeRecord{}, nil
	}
	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var where []string
	var args []any
	from := `knowledge_records k`
	order := `k.created_at DESC`
	if match := ftsQuery(q.Text); match != "" {
		from = `knowledge_records k JOIN knowledge_fts f ON f.rowid = k.rowid`
		where = append(where, `knowledge_fts MATCH ?`)
		args = append(args, match)
		order = `bm25(knowledge_fts), k.created_at DESC`
	}
	if q.ProjectIDs != nil {
		where = append(where, `k.project_id IN (`+placeholders(len(q.ProjectIDs))+`)`)
		for _, id := range q.ProjectIDs {
			args = append(args, id)
		}
	}
	if q.WOID != "" {
		where = append(where, `k.wo_id = ?`)
		args = append(args, q.WOID)
	}
	if q.Trust != "" {
		where = append(where, `k.trust = ?`)
		args = append(args, string(q.Trust))
	}
	query := `SELECT ` + kCols + ` FROM ` + from
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY ` + order + ` LIMIT ?`
	args = append(args, limit)

	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.KnowledgeRecord{}
	for rows.Next() {
		k, err := scanK(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

func scanK(scan rowScanner) (*model.KnowledgeRecord, error) {
	var k model.KnowledgeRecord
	var woID sql.NullString
	var trust, refs string
	var createdAt int64
	if err := scan(&k.ID, &k.WorkspaceID, &k.ProjectID, &woID, &k.RepoURL, &k.CommitSHA, &k.Kind, &trust,
		&k.Title, &k.Body, &k.Actor, &refs, &createdAt); err != nil {
		return nil, err
	}
	k.WOID = woID.String
	k.Trust = model.Trust(trust)
	_ = json.Unmarshal([]byte(refs), &k.SourceRefs)
	if k.SourceRefs == nil {
		k.SourceRefs = []string{}
	}
	k.CreatedAt = time.Unix(0, createdAt)
	return &k, nil
}

// ftsQuery turns free text into an FTS5 OR-query of quoted terms so user
// input can never inject FTS operators.
func ftsQuery(text string) string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	seen := map[string]bool{}
	var terms []string
	for _, w := range words {
		if len(w) < 3 || seen[w] {
			continue
		}
		seen[w] = true
		terms = append(terms, `"`+w+`"`)
	}
	return strings.Join(terms, " OR ")
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
