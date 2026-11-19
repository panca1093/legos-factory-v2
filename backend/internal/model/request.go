package model

import (
	"fmt"
	"regexp"
	"strings"
)

var sourceRefPattern = regexp.MustCompile(`^jira:[A-Z][A-Z0-9_]+-[0-9]+$`)

type CreateProjectRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	RepoURL     string `json:"repo_url"`
}

func (r *CreateProjectRequest) Validate() error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len(r.Name) > 200 {
		return fmt.Errorf("name too long")
	}
	return nil
}

type CreateWorkOrderRequest struct {
	ProjectID      string `json:"project_id"`
	Title          string `json:"title"`
	Objective      string `json:"objective"`
	Acceptance     string `json:"acceptance"`
	BuilderModel   string `json:"builder_model"`
	InspectorModel string `json:"inspector_model"`
	SourceRef      string `json:"source_ref"`
	SourceChatID   string `json:"source_chat_id"`
	BaseBranch     string `json:"base_branch"`
}

var branchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

func (r *CreateWorkOrderRequest) Validate() error {
	r.Title = strings.TrimSpace(r.Title)
	r.Objective = strings.TrimSpace(r.Objective)
	r.Acceptance = strings.TrimSpace(r.Acceptance)
	switch {
	case r.Title == "":
		return fmt.Errorf("title is required")
	case r.Objective == "":
		return fmt.Errorf("objective is required")
	case r.ProjectID == "":
		return fmt.Errorf("project_id is required")
	}
	r.BaseBranch = strings.TrimSpace(r.BaseBranch)
	if b := r.BaseBranch; b != "" && (!branchPattern.MatchString(b) || strings.Contains(b, "..") || strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".lock") || strings.Contains(b, "//")) {
		return fmt.Errorf("base_branch is not a valid branch name")
	}
	r.SourceRef = strings.TrimSpace(r.SourceRef)
	if r.SourceRef != "" && !sourceRefPattern.MatchString(r.SourceRef) {
		return fmt.Errorf("source_ref must look like jira:ABC-123")
	}
	return nil
}

type PlanDecisionRequest struct {
	Decision string `json:"decision"`
	Notes    string `json:"notes"`
}

type ClarificationRequest struct {
	Answer string `json:"answer"`
}

type AddMemberRequest struct {
	Email string `json:"email"`
}

type PRDecisionRequest struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}
