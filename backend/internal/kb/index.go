// Package kb keeps the knowledge index: it parses OKF-style markdown from a
// project repository or the factory's own bundle, embeds it locally, and
// syncs pointers into an Index. Markdown files are the system of record;
// the index can be rebuilt from them.
package kb

import (
	"context"

	"legos-factory/backend/internal/model"
)

// Index is the derived store (implemented by store.KBRepo). It never keeps
// document text: only metadata, links, a contentless keyword index and a vector.
type Index interface {
	// Hashes returns path → content_hash for every doc of one source and project.
	Hashes(ctx context.Context, source, projectID string) (map[string]string, error)
	// Upsert replaces a doc, its links, its keyword entry and its vector.
	Upsert(ctx context.Context, d model.KBParsed, vec []float32) error
	// Touch records that an unchanged doc was seen at commitSHA.
	Touch(ctx context.Context, id, commitSHA string) error
	Delete(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (*model.KBDoc, error)
	// Search ranks by keyword (bm25) and vector (cosine) fused with RRF, then
	// adds docs linked from the hits as see-also; ties break by path.
	Search(ctx context.Context, q model.KBQuery, vec []float32) ([]model.KBHit, error)
	// List filters without ranking, ordered by source then path.
	List(ctx context.Context, q model.KBQuery) ([]model.KBDoc, error)
	// Backlinks lists docs that link to id.
	Backlinks(ctx context.Context, id string) ([]model.KBDoc, error)
}

// Embedder turns text into a fixed-size, L2-normalised vector.
type Embedder interface {
	Name() string
	Dim() int
	Embed(text string) []float32
}
