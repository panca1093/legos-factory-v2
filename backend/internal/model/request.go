package model

import (
	"fmt"
	"strings"
)

type CreateProjectRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	RepoURL     string `json:"repo_url"`
	Language    string `json:"language"`
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
	Autonomy       string `json:"autonomy"`
	BuilderModel   string `json:"builder_model"`
	InspectorModel string `json:"inspector_model"`
}

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
	switch r.Autonomy {
	case "", "guided", "assisted", "delegated":
	default:
		return fmt.Errorf("autonomy must be guided, assisted or delegated")
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

type PRDecisionRequest struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}
