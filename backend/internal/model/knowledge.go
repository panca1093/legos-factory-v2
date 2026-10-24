package model

import "time"

const DefaultWorkspaceID = "ws_default"

// Trust separates evidence the application verified, decisions a human made,
// and interpretations an agent produced. Only the first two are authoritative.
type Trust string

const (
	TrustVerified         Trust = "verified"
	TrustHumanDecision    Trust = "human_decision"
	TrustAIInterpretation Trust = "ai_interpretation"
)

const (
	KnowledgePlanApproved       = "plan_approved"
	KnowledgePlanRevision       = "plan_revision"
	KnowledgeClarification      = "clarification"
	KnowledgeArchitectReview    = "architect_review"
	KnowledgeImplementation     = "implementation"
	KnowledgeVerification       = "verification"
	KnowledgeFindings           = "findings"
	KnowledgeReadiness          = "readiness"
	KnowledgePRDecision         = "pr_decision"
	KnowledgePROpened           = "pr_opened"
	KnowledgeWorkOrderCompleted = "work_order_completed"
	KnowledgeSummary            = "summary"
)

type KnowledgeRecord struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	ProjectID   string    `json:"project_id"`
	WOID        string    `json:"work_order_id,omitempty"`
	RepoURL     string    `json:"repo_url,omitempty"`
	CommitSHA   string    `json:"commit_sha,omitempty"`
	Kind        string    `json:"kind"`
	Trust       Trust     `json:"trust"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	Actor       string    `json:"actor"`
	SourceRefs  []string  `json:"source_refs"`
	CreatedAt   time.Time `json:"created_at"`
}

// KnowledgeQuery filters records. ProjectIDs nil means unrestricted; an
// empty non-nil slice matches nothing.
type KnowledgeQuery struct {
	ProjectIDs []string
	WOID       string
	Trust      Trust
	Text       string
	Limit      int
}

type ProjectMember struct {
	ProjectID string    `json:"project_id"`
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Role      Role      `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}
