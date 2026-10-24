package model

import "time"

type Project struct {
	ID             string    `json:"id"`
	WorkspaceID    string    `json:"workspace_id"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	RepoURL        string    `json:"repo_url"`
	Language       string    `json:"language"`
	QualityProfile string    `json:"quality_profile"` // JSON: [{"name","command"}]
	CreatedAt      time.Time `json:"created_at"`
}
