package model

import "time"

// Tri is a three-valued answer for capabilities that cannot always be probed.
type Tri string

const (
	TriYes     Tri = "yes"
	TriNo      Tri = "no"
	TriUnknown Tri = "unknown"
)

// HostUser is an account on a git host. ID is the GitHub numeric id, GitLab id or Bitbucket uuid.
type HostUser struct {
	Login string `json:"login"`
	ID    string `json:"id"`
}

type TokenInfo struct {
	Scopes    []string   `json:"scopes"`
	CanRead   bool       `json:"can_read"`
	CanWrite  Tri        `json:"can_write"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// PRFilter selects open pull requests by author and/or requested reviewer; a zero HostUser is unset.
type PRFilter struct {
	Author   HostUser
	Reviewer HostUser
}

type PRSummary struct {
	Number             int        `json:"number"`
	Title              string     `json:"title"`
	URL                string     `json:"url"`
	Author             HostUser   `json:"author"`
	Head               string     `json:"head"`
	Base               string     `json:"base"`
	HeadSHA            string     `json:"head_sha"`
	Draft              bool       `json:"draft"`
	RequestedReviewers []HostUser `json:"requested_reviewers"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// HostReview is a review the user already submitted on the host.
type HostReview struct {
	Verdict  string `json:"verdict"` // comment|approve|request_changes
	CommitID string `json:"commit_id"`
}

type PRDetail struct {
	PRSummary
	BaseSHA        string      `json:"base_sha"`
	StartSHA       string      `json:"start_sha"` // GitLab diff_refs need base, start and head
	Body           string      `json:"body"`
	Additions      int         `json:"additions"`
	Deletions      int         `json:"deletions"`
	ChangedFiles   int         `json:"changed_files"`
	CI             string      `json:"ci"` // passing|failing|running|none
	MyLatestReview *HostReview `json:"my_latest_review,omitempty"`
	SourceRepo     string      `json:"source_repo"` // != repo for fork PRs
}

type PRFile struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path"`
	Status    string `json:"status"` // added|modified|deleted|renamed
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch"` // unified text with ---/+++ header; "" when binary or too large
	Truncated bool   `json:"truncated"`
}

const (
	VerdictComment        = "comment"
	VerdictApprove        = "approve"
	VerdictRequestChanges = "request_changes"
)

type ReviewSubmission struct {
	HeadSHA  string
	Body     string
	Verdict  string
	Comments []InlineComment
}

type InlineComment struct {
	DraftID   string
	Path      string
	OldPath   string
	Side      string // new|old
	Line      int
	OldLine   int // context lines: both set (GitLab needs both)
	Body      string
	PostedRef string // set when already posted (Bitbucket retry)
}

type PostedReview struct {
	Ref        string            `json:"ref"`
	Comments   map[string]string `json:"comments"` // draft id → provider ref
	Failed     map[string]string `json:"failed"`   // draft id → provider message
	VerdictErr string            `json:"verdict_err,omitempty"`
}

// PRKey identifies a pull request on a host.
type PRKey struct {
	Provider string
	Host     string
	Repo     string
	Number   int
}

// ReviewIdentity is the account pinned to post reviews for a user on a host.
type ReviewIdentity struct {
	UserID       string    `json:"user_id"`
	Provider     string    `json:"provider"`
	Host         string    `json:"host"`
	Source       string    `json:"source"` // connection|gh|glab
	ConnectionID string    `json:"connection_id,omitempty"`
	Login        string    `json:"login"`
	HostUserID   string    `json:"host_user_id,omitempty"`
	PinnedAt     time.Time `json:"pinned_at"`
}

type PRReview struct {
	ID            string     `json:"id"`
	UserID        string     `json:"user_id"`
	ProjectID     string     `json:"project_id"`
	Provider      string     `json:"provider"`
	Host          string     `json:"host"`
	Repo          string     `json:"repo"`
	Number        int        `json:"number"`
	HeadSHA       string     `json:"head_sha"` // head the current drafts were made on
	Summary       string     `json:"summary"`
	Verdict       string     `json:"verdict"`
	PostedAt      *time.Time `json:"posted_at,omitempty"`
	PostedHeadSHA string     `json:"posted_head_sha"`
	PostedVerdict string     `json:"posted_verdict"`
	PostedRef     string     `json:"posted_ref"`
	PostedAs      string     `json:"posted_as"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

const (
	ReviewRunRunning   = "running"
	ReviewRunCompleted = "completed"
	ReviewRunFailed    = "failed"
)

type PRReviewRun struct {
	ID               string     `json:"id"`
	ReviewID         string     `json:"review_id"`
	HeadSHA          string     `json:"head_sha"`
	State            string     `json:"state"`
	Agent            string     `json:"agent"`
	Harness          string     `json:"harness"`
	Model            string     `json:"model"`
	Summary          string     `json:"summary"`
	Error            string     `json:"error"`
	TokensIn         int64      `json:"tokens_in"`
	TokensOut        int64      `json:"tokens_out"`
	CacheReadTokens  int64      `json:"cache_read_tokens"`
	CacheWriteTokens int64      `json:"cache_write_tokens"`
	CostMicros       int64      `json:"cost_micros"`
	StartedAt        time.Time  `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
}

const (
	DraftPending    = "pending"
	DraftAccepted   = "accepted"
	DraftDiscarded  = "discarded"
	DraftSuperseded = "superseded"
	DraftOutdated   = "outdated"
	DraftPosted     = "posted"
)

type PRDraft struct {
	ID        string    `json:"id"`
	ReviewID  string    `json:"review_id"`
	RunID     string    `json:"run_id"`
	Path      string    `json:"path"`
	OldPath   string    `json:"old_path"`
	Side      string    `json:"side"`
	Line      int       `json:"line"`
	OldLine   int       `json:"old_line"`
	Inline    bool      `json:"inline"` // false = General (not anchorable)
	HeadSHA   string    `json:"head_sha"`
	Severity  string    `json:"severity"` // blocker|major|minor|info
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	AIBody    string    `json:"ai_body"` // kept to show "edited"
	Status    string    `json:"status"`
	PostedRef string    `json:"posted_ref"`
	PostError string    `json:"post_error"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CLILoginStep is one step of an in-app `gh auth login` / `glab auth login` flow.
type CLILoginStep struct {
	Kind    string `json:"kind"` // code|url|done|failed|fallback
	Code    string `json:"code,omitempty"`
	URL     string `json:"url,omitempty"`
	Login   string `json:"login,omitempty"`
	Message string `json:"message,omitempty"`
}
