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
	PublishVia     string    `json:"publish_via"`           // computed: connection | gh | none
	GitHubRepo     string    `json:"github_repo"`           // computed: owner/repo in gh mode
	CreatedAt      time.Time `json:"created_at"`
}

// LocalRepo describes a folder on the host: the folder itself, or when it is
// not a git repo, the git repos among its direct children.
type LocalRepo struct {
	Path          string      `json:"path"`
	Name          string      `json:"name"`
	IsGit         bool        `json:"is_git"`
	GitHubRepo    string      `json:"github_repo"`
	DefaultBranch string      `json:"default_branch"`
	Language      string      `json:"language"`
	Repos         []LocalRepo `json:"repos"`
}
