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
	GitConnection  string    `json:"git_connection_id,omitempty"`
	RemoteRepo     string    `json:"remote_repo,omitempty"` // provider slug, e.g. org/repo
	CreatedAt      time.Time `json:"created_at"`
}
