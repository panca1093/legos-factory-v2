package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"legos-factory/backend/internal/model"
)

// Jira reads issues from Jira Cloud (https://<site>.atlassian.net) with an
// account email and API token. It never modifies issues.
type Jira struct {
	api  api
	base string
}

func NewJira(baseURL, email, token string, hc *http.Client) *Jira {
	return &Jira{api: newAPI("jira", baseURL, hc, basic(email, token)), base: strings.TrimRight(baseURL, "/")}
}

func (j *Jira) CheckAuth(ctx context.Context) (string, error) {
	var me struct {
		DisplayName  string `json:"displayName"`
		EmailAddress string `json:"emailAddress"`
	}
	if err := j.api.do(ctx, http.MethodGet, "/rest/api/3/myself", nil, &me); err != nil {
		return "", err
	}
	if me.EmailAddress != "" {
		return me.EmailAddress, nil
	}
	return me.DisplayName, nil
}

func (j *Jira) GetIssue(ctx context.Context, key string) (model.Issue, error) {
	key = strings.ToUpper(strings.TrimSpace(key))
	if !model.IssueKeyPattern.MatchString(key) {
		return model.Issue{}, fmt.Errorf("issue key must look like ABC-123, got %q", key)
	}
	var raw struct {
		Key    string `json:"key"`
		Fields struct {
			Summary     string          `json:"summary"`
			Description json.RawMessage `json:"description"`
		} `json:"fields"`
	}
	if err := j.api.do(ctx, http.MethodGet, "/rest/api/3/issue/"+key+"?fields=summary,description", nil, &raw); err != nil {
		return model.Issue{}, err
	}
	return model.Issue{Key: raw.Key, Title: raw.Fields.Summary, Description: adfText(raw.Fields.Description), URL: j.base + "/browse/" + raw.Key}, nil
}

// adfText flattens Atlassian Document Format into plain text: one line per
// block, list items prefixed with "- ".
func adfText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var node adfNode
	if json.Unmarshal(raw, &node) != nil {
		return ""
	}
	var b strings.Builder
	node.write(&b)
	return strings.TrimSpace(b.String())
}

type adfNode struct {
	Type    string    `json:"type"`
	Text    string    `json:"text"`
	Content []adfNode `json:"content"`
}

func (n adfNode) write(b *strings.Builder) {
	switch n.Type {
	case "text":
		b.WriteString(n.Text)
		return
	case "hardBreak":
		b.WriteString("\n")
		return
	case "listItem":
		b.WriteString("- ")
	}
	for _, c := range n.Content {
		c.write(b)
	}
	switch n.Type {
	case "paragraph", "heading", "codeBlock", "blockquote":
		b.WriteString("\n")
	}
}
