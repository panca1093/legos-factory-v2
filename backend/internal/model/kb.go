package model

import "time"

// Knowledge index sources: a project's repository, or the factory's own bundle.
const (
	KBSourceRepo    = "repo"
	KBSourceFactory = "factory"
)

// OKF lifecycle statuses.
const (
	KBStatusDraft      = "draft"
	KBStatusStable     = "stable"
	KBStatusDeprecated = "deprecated"
)

// Trust tiers derived from OKF `verified`: none, only non-human actors, or a human: actor.
const (
	KBTrustUnverified = "unverified"
	KBTrustMachine    = "machine"
	KBTrustHuman      = "human"
)

// Well-known OKF `type` values the factory reads or writes.
const (
	KBTypePlaybook      = "Playbook"
	KBTypeReviewerRoute = "Reviewer Routing"
	KBTypeJournalEntry  = "Journal Entry"
	KBTypeBrief         = "Brief"
	KBTypeSpec          = "Spec"
	KBTypeTaskDAG       = "Task DAG"
	KBTypeTaskSummary   = "Task Summary"
	KBTypeNote          = "Note"
)

// KBDoc is one indexed markdown document: pointers and metadata, never its text.
type KBDoc struct {
	ID          string         `json:"id"` // source + ":" + project_id + ":" + path; project_id is "" for factory-wide docs
	Source      string         `json:"source"`
	ProjectID   string         `json:"project_id,omitempty"`
	Path        string         `json:"path"` // relative to the source root, forward slashes
	CommitSHA   string         `json:"commit_sha"`
	ContentHash string         `json:"content_hash"`
	Type        string         `json:"type"`
	Status      string         `json:"status"`
	Trust       string         `json:"trust"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	ReadOnly    bool           `json:"read_only"`       // computed from meta origin: work_order
	Meta        map[string]any `json:"meta"`            // frontmatter keys other than type, title, description, status
	Links       []string       `json:"links,omitempty"` // target doc ids, resolved within the same source and project
	IndexedAt   time.Time      `json:"indexed_at"`
}

// KBParsed is a document as read from disk: the index row plus the text that
// feeds the keyword index and the vector, neither of which the index stores.
type KBParsed struct {
	KBDoc
	Headings string
	Body     string
}

// KBQuery filters and ranks documents. Factory-wide docs (empty ProjectID)
// are always eligible alongside the given project's docs.
type KBQuery struct {
	ProjectID string
	Text      string   // empty: no ranking, ordered by path
	Sources   []string // empty: all
	Types     []string // empty: all
	OriginID  string   // empty: any; else only docs whose frontmatter origin_id matches
	Statuses  []string // empty: stable only
	Limit     int      // <= 0: 8 for ranked search, 200 for listing
}

// KBHit is one ranked result. SeeAlso is set when the doc was added because
// a ranked hit links to it; it holds that hit's id.
type KBHit struct {
	Doc     KBDoc   `json:"doc"`
	Score   float64 `json:"score"`
	SeeAlso string  `json:"see_also,omitempty"`
}

// ReviewerRoute is one entry of a `type: Reviewer Routing` document's `routes`.
// A path starting with "!" excludes; globs support "**".
type ReviewerRoute struct {
	Paths     []string `json:"paths" yaml:"paths"`
	Reviewers []string `json:"reviewers" yaml:"reviewers"`
}
