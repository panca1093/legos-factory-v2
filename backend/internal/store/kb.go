package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"legos-factory/backend/internal/model"
)

const (
	kbCandidateCap  = 50
	kbRRFK          = 60
	kbDefaultSearch = 8
	kbDefaultList   = 200
	kbSeeAlsoCap    = 4
	defaultEmbedder = "hash-ngram-v1"
)

// KBRepo is the SQLite knowledge index; it implements kb.Index.
type KBRepo struct {
	DB       *sql.DB
	Embedder string // recorded next to each vector; defaults to hash-ngram-v1
}

type kbQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const kbCols = `d.id, d.source, d.project_id, d.path, d.commit_sha, d.content_hash, d.type, d.status, d.trust, d.title, d.description, d.meta, d.indexed_at`

func (r *KBRepo) Hashes(ctx context.Context, source, projectID string) (map[string]string, error) {
	q := `SELECT path, content_hash FROM kb_docs WHERE source = ? AND project_id IS NULL`
	args := []any{source}
	if projectID != "" {
		q = `SELECT path, content_hash FROM kb_docs WHERE source = ? AND project_id = ?`
		args = append(args, projectID)
	}
	rows, err := r.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, h string
		if err := rows.Scan(&p, &h); err != nil {
			return nil, err
		}
		out[p] = h
	}
	return out, rows.Err()
}

func (r *KBRepo) Upsert(ctx context.Context, d model.KBParsed, vec []float32) error {
	meta := d.Meta
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `INSERT INTO kb_docs (id, source, project_id, path, commit_sha, content_hash, type, status, trust, title, description, meta, indexed_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET source=excluded.source, project_id=excluded.project_id, path=excluded.path, commit_sha=excluded.commit_sha,
			content_hash=excluded.content_hash, type=excluded.type, status=excluded.status, trust=excluded.trust, title=excluded.title,
			description=excluded.description, meta=excluded.meta, indexed_at=excluded.indexed_at`,
		d.ID, d.Source, nullStr(d.ProjectID), d.Path, d.CommitSHA, d.ContentHash, d.Type, d.Status, d.Trust, d.Title, d.Description,
		string(metaJSON), time.Now().Unix()); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM kb_links WHERE from_doc = ?`, d.ID); err != nil {
		return err
	}
	for _, to := range d.Links {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO kb_links (from_doc, to_doc) VALUES (?,?)`, d.ID, to); err != nil {
			return err
		}
	}

	var rowid int64
	err = tx.QueryRowContext(ctx, `SELECT fts_rowid FROM kb_fts_map WHERE doc_id = ?`, d.ID).Scan(&rowid)
	switch {
	case err == nil:
		if _, err := tx.ExecContext(ctx, `DELETE FROM kb_fts WHERE rowid = ?`, rowid); err != nil {
			return err
		}
	case errors.Is(err, sql.ErrNoRows):
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(fts_rowid), 0) + 1 FROM kb_fts_map`).Scan(&rowid); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO kb_fts_map (doc_id, fts_rowid) VALUES (?,?)`, d.ID, rowid); err != nil {
			return err
		}
	default:
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO kb_fts (rowid, title, description, headings, body) VALUES (?,?,?,?,?)`,
		rowid, d.Title, d.Description, d.Headings, d.Body); err != nil {
		return err
	}

	embedder := r.Embedder
	if embedder == "" {
		embedder = defaultEmbedder
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO kb_vectors (doc_id, embedder, vec) VALUES (?,?,?)
		ON CONFLICT(doc_id) DO UPDATE SET embedder=excluded.embedder, vec=excluded.vec`, d.ID, embedder, encodeVec(vec)); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *KBRepo) Touch(ctx context.Context, id, commitSHA string) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE kb_docs SET commit_sha = ?, indexed_at = ? WHERE id = ?`, commitSHA, time.Now().Unix(), id)
	return err
}

func (r *KBRepo) Delete(ctx context.Context, id string) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// the contentless FTS table is not reached by FK cascades
	if _, err := tx.ExecContext(ctx, `DELETE FROM kb_fts WHERE rowid IN (SELECT fts_rowid FROM kb_fts_map WHERE doc_id = ?)`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM kb_docs WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Get returns nil, nil when the doc is not indexed.
func (r *KBRepo) Get(ctx context.Context, id string) (*model.KBDoc, error) {
	docs, err := r.queryDocs(ctx, r.DB, `SELECT `+kbCols+` FROM kb_docs d WHERE d.id = ?`, id)
	if err != nil || len(docs) == 0 {
		return nil, err
	}
	return &docs[0], nil
}

func (r *KBRepo) List(ctx context.Context, q model.KBQuery) ([]model.KBDoc, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = kbDefaultList
	}
	where, args := kbFilter(q)
	args = append(args, limit)
	return r.queryDocs(ctx, r.DB, `SELECT `+kbCols+` FROM kb_docs d WHERE `+where+` ORDER BY d.source, d.path, d.id LIMIT ?`, args...)
}

func (r *KBRepo) Backlinks(ctx context.Context, id string) ([]model.KBDoc, error) {
	return r.queryDocs(ctx, r.DB, `SELECT `+kbCols+` FROM kb_docs d JOIN kb_links l ON l.from_doc = d.id WHERE l.to_doc = ? ORDER BY d.path, d.id`, id)
}

func (r *KBRepo) Search(ctx context.Context, q model.KBQuery, vec []float32) ([]model.KBHit, error) {
	match := ftsQuery(q.Text)
	if match == "" && isZeroVec(vec) {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = kbDefaultSearch
	}
	where, fargs := kbFilter(q)

	docs := map[string]model.KBDoc{}
	score := map[string]float64{}

	if match != "" {
		args := append([]any{match}, fargs...)
		args = append(args, kbCandidateCap)
		ranked, err := r.queryDocs(ctx, r.DB, `SELECT `+kbCols+` FROM kb_fts
			JOIN kb_fts_map m ON m.fts_rowid = kb_fts.rowid JOIN kb_docs d ON d.id = m.doc_id
			WHERE kb_fts MATCH ? AND `+where+` ORDER BY bm25(kb_fts), d.path, d.id LIMIT ?`, args...)
		if err != nil {
			return nil, err
		}
		for i, d := range ranked {
			docs[d.ID] = d
			score[d.ID] += 1.0 / float64(kbRRFK+i+1)
		}
	}

	if !isZeroVec(vec) {
		cands, err := r.queryDocs(ctx, r.DB, `SELECT `+kbCols+` FROM kb_docs d WHERE `+where, fargs...)
		if err != nil {
			return nil, err
		}
		vecs, err := r.loadVectors(ctx, cands)
		if err != nil {
			return nil, err
		}
		type scored struct {
			doc model.KBDoc
			sim float64
		}
		var sims []scored
		for _, d := range cands {
			v, ok := vecs[d.ID]
			if !ok || len(v) != len(vec) {
				continue
			}
			sims = append(sims, scored{d, dot(vec, v)})
		}
		sort.SliceStable(sims, func(i, j int) bool {
			if sims[i].sim != sims[j].sim {
				return sims[i].sim > sims[j].sim
			}
			if sims[i].doc.Path != sims[j].doc.Path {
				return sims[i].doc.Path < sims[j].doc.Path
			}
			return sims[i].doc.ID < sims[j].doc.ID
		})
		if len(sims) > kbCandidateCap {
			sims = sims[:kbCandidateCap]
		}
		for i, s := range sims {
			docs[s.doc.ID] = s.doc
			score[s.doc.ID] += 1.0 / float64(kbRRFK+i+1)
		}
	}

	ids := make([]string, 0, len(docs))
	for id := range docs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := docs[ids[i]], docs[ids[j]]
		if score[a.ID] != score[b.ID] {
			return score[a.ID] > score[b.ID]
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.ID < b.ID
	})
	if len(ids) > limit {
		ids = ids[:limit]
	}

	hits := make([]model.KBHit, 0, len(ids))
	present := map[string]bool{}
	for _, id := range ids {
		d := docs[id]
		if err := r.fillLinks(ctx, r.DB, &d); err != nil {
			return nil, err
		}
		hits = append(hits, model.KBHit{Doc: d, Score: score[id]})
		present[id] = true
	}

	seeAlso := 0
	for _, h := range hits[:len(hits):len(hits)] {
		for _, to := range h.Doc.Links {
			if seeAlso >= kbSeeAlsoCap {
				return hits, nil
			}
			if present[to] {
				continue
			}
			linked, err := r.queryDocs(ctx, r.DB, `SELECT `+kbCols+` FROM kb_docs d WHERE d.id = ? AND `+where, append([]any{to}, fargs...)...)
			if err != nil {
				return nil, err
			}
			if len(linked) == 0 {
				continue
			}
			present[to] = true
			seeAlso++
			hits = append(hits, model.KBHit{Doc: linked[0], SeeAlso: h.Doc.ID})
		}
	}
	return hits, nil
}

// kbFilter builds the shared candidate WHERE over alias d.
func kbFilter(q model.KBQuery) (string, []any) {
	var where []string
	var args []any
	if q.ProjectID == "" {
		where = append(where, `d.project_id IS NULL`)
	} else {
		where = append(where, `(d.project_id IS NULL OR d.project_id = ?)`)
		args = append(args, q.ProjectID)
	}
	in := func(col string, vals []string) {
		if len(vals) == 0 {
			return
		}
		where = append(where, col+` IN (`+placeholders(len(vals))+`)`)
		for _, v := range vals {
			args = append(args, v)
		}
	}
	in("d.source", q.Sources)
	in("d.type", q.Types)
	statuses := q.Statuses
	if len(statuses) == 0 {
		statuses = []string{model.KBStatusStable}
	}
	in("d.status", statuses)
	return strings.Join(where, " AND "), args
}

func (r *KBRepo) queryDocs(ctx context.Context, db kbQuerier, query string, args ...any) ([]model.KBDoc, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var docs []model.KBDoc
	for rows.Next() {
		d, err := scanKB(rows.Scan)
		if err != nil {
			rows.Close()
			return nil, err
		}
		docs = append(docs, *d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for i := range docs {
		if err := r.fillLinks(ctx, db, &docs[i]); err != nil {
			return nil, err
		}
	}
	return docs, nil
}

func (r *KBRepo) fillLinks(ctx context.Context, db kbQuerier, d *model.KBDoc) error {
	rows, err := db.QueryContext(ctx, `SELECT to_doc FROM kb_links WHERE from_doc = ? ORDER BY to_doc`, d.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	d.Links = nil
	for rows.Next() {
		var to string
		if err := rows.Scan(&to); err != nil {
			return err
		}
		d.Links = append(d.Links, to)
	}
	return rows.Err()
}

func (r *KBRepo) loadVectors(ctx context.Context, docs []model.KBDoc) (map[string][]float32, error) {
	out := map[string][]float32{}
	if len(docs) == 0 {
		return out, nil
	}
	const batch = 500
	for start := 0; start < len(docs); start += batch {
		end := min(start+batch, len(docs))
		args := make([]any, 0, end-start)
		for _, d := range docs[start:end] {
			args = append(args, d.ID)
		}
		rows, err := r.DB.QueryContext(ctx, `SELECT doc_id, vec FROM kb_vectors WHERE doc_id IN (`+placeholders(len(args))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			var blob []byte
			if err := rows.Scan(&id, &blob); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = decodeVec(blob)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func scanKB(scan rowScanner) (*model.KBDoc, error) {
	var d model.KBDoc
	var project sql.NullString
	var meta string
	var indexedAt int64
	if err := scan(&d.ID, &d.Source, &project, &d.Path, &d.CommitSHA, &d.ContentHash, &d.Type, &d.Status, &d.Trust, &d.Title, &d.Description, &meta, &indexedAt); err != nil {
		return nil, err
	}
	d.ProjectID = project.String
	d.Meta = map[string]any{}
	if err := json.Unmarshal([]byte(meta), &d.Meta); err != nil {
		return nil, err
	}
	d.IndexedAt = time.Unix(indexedAt, 0)
	return &d, nil
}

func encodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}

func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

func isZeroVec(v []float32) bool {
	for _, f := range v {
		if f != 0 {
			return false
		}
	}
	return true
}

func dot(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}
