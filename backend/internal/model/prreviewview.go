package model

import "time"

// Wire shapes of the Review PR API (docs/review-pr/CONTRACT.md).

const (
	ReadinessReady    = "ready"
	ReadinessReadOnly = "read_only"
	ReadinessUnknown  = "unknown"
	ReadinessExpired  = "expired"
	ReadinessInvalid  = "invalid"
)

const (
	LabelAI      = "ai"
	LabelYours   = "yours"
	LabelNew     = "new"
	LabelApprove = "approve"
	LabelNone    = "none"
)

const (
	RoleAuthor   = "author"
	RoleReviewer = "reviewer"
	RoleBoth     = "both"
)

const (
	IdentitySourceConnection = "connection"
	IdentitySourceGH         = "gh"
)

const (
	ErrCodeNoIdentity = "no_identity"
	ErrCodeRateLimit  = "rate_limited"
	ErrCodeProvider   = "provider"
	ErrCodeNotFound   = "not_found"
	ErrCodeForbidden  = "forbidden"
)

type IdentityView struct {
	Provider  string     `json:"provider"`
	Host      string     `json:"host"`
	Source    string     `json:"source"`
	Login     string     `json:"login"`
	Readiness string     `json:"readiness"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type ProviderErr struct {
	Code    string     `json:"code"`
	Message string     `json:"message"`
	Until   *time.Time `json:"until,omitempty"`
}

type PRRow struct {
	Number        int       `json:"number"`
	Title         string    `json:"title"`
	URL           string    `json:"url"`
	Author        HostUser  `json:"author"`
	Head          string    `json:"head"`
	Base          string    `json:"base"`
	Draft         bool      `json:"draft"`
	UpdatedAt     time.Time `json:"updated_at"`
	Role          string    `json:"role"`
	Label         string    `json:"label"`
	PendingDrafts int       `json:"pending_drafts"`
	Running       bool      `json:"running"`
	FactoryWOID   string    `json:"factory_wo_id,omitempty"`
}

type PRListView struct {
	Identity *IdentityView `json:"identity"`
	Rows     []PRRow       `json:"rows"`
	Stale    bool          `json:"stale"`
	Error    *ProviderErr  `json:"error,omitempty"`
}

type ProjectPRs struct {
	Project  ReviewProject `json:"project"`
	Identity *IdentityView `json:"identity"`
	Rows     []PRRow       `json:"rows"`
	Error    *ProviderErr  `json:"error,omitempty"`
}

type ReviewProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Availability struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

type Verdicts struct {
	Approve        Availability `json:"approve"`
	RequestChanges Availability `json:"request_changes"`
}

type PRDetailView struct {
	PR       PRDetail      `json:"pr"`
	Identity *IdentityView `json:"identity"`
	Review   *PRReview     `json:"review"`
	Run      *PRReviewRun  `json:"run"`
	Verdicts Verdicts      `json:"verdicts"`
	AI       Availability  `json:"ai"`
}

type PRFileView struct {
	PRFile
	Viewed bool `json:"viewed"`
}

type ViewedCount struct {
	Viewed int `json:"viewed"`
	Total  int `json:"total"`
}

// PostExpect is what the client saw before it asked to post; the server posts only when it still holds.
type PostExpect struct {
	Login    string `json:"login"`
	HeadSHA  string `json:"head_sha"`
	Comments int    `json:"comments"`
	Verdict  string `json:"verdict"`
}

type PostRequest struct {
	Confirm bool       `json:"confirm"`
	Expect  PostExpect `json:"expect"`
}

type ConnectionChoice struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Login     string     `json:"login"`
	Readiness string     `json:"readiness"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type CLIStatus struct {
	Tool      string   `json:"tool"`
	Installed bool     `json:"installed"`
	Users     []string `json:"users"`
}

type IdentityOptions struct {
	Provider    string             `json:"provider"`
	Host        string             `json:"host"`
	Pinned      *ReviewIdentity    `json:"pinned"`
	Connections []ConnectionChoice `json:"connections"`
	CLI         *CLIStatus         `json:"cli"`
}

type PinRequest struct {
	Provider     string `json:"provider"`
	Host         string `json:"host"`
	Source       string `json:"source"`
	ConnectionID string `json:"connection_id"`
	Login        string `json:"login"`
}

type SummaryRow struct {
	ProjectID    string        `json:"project_id"`
	NeedsYou     int           `json:"needs_you"`
	WORunning    int           `json:"wo_running"`
	PRsForMe     *int          `json:"prs_for_me"`
	Shipped30d   int           `json:"shipped_30d"`
	LastActivity *time.Time    `json:"last_activity"`
	Identity     *IdentityView `json:"identity"`
}
