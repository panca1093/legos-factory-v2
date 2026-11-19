package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"legos-factory/backend/internal/model"
)

type glUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

func (u glUser) host() model.HostUser {
	h := model.HostUser{Login: u.Username}
	if u.ID != 0 {
		h.ID = strconv.FormatInt(u.ID, 10)
	}
	return h
}

type glMRFull struct {
	IID             int      `json:"iid"`
	Title           string   `json:"title"`
	WebURL          string   `json:"web_url"`
	Description     string   `json:"description"`
	Author          glUser   `json:"author"`
	SourceBranch    string   `json:"source_branch"`
	TargetBranch    string   `json:"target_branch"`
	SHA             string   `json:"sha"`
	Draft           bool     `json:"draft"`
	WorkInProgress  bool     `json:"work_in_progress"`
	Reviewers       []glUser `json:"reviewers"`
	UpdatedAt       string   `json:"updated_at"`
	ChangesCount    string   `json:"changes_count"`
	SourceProjectID int64    `json:"source_project_id"`
	TargetProjectID int64    `json:"target_project_id"`
	DiffRefs        *struct {
		BaseSHA  string `json:"base_sha"`
		StartSHA string `json:"start_sha"`
		HeadSHA  string `json:"head_sha"`
	} `json:"diff_refs"`
	HeadPipeline *struct {
		Status string `json:"status"`
	} `json:"head_pipeline"`
}

func (m glMRFull) summary() model.PRSummary {
	s := model.PRSummary{
		Number: m.IID, Title: m.Title, URL: m.WebURL, Author: m.Author.host(),
		Head: m.SourceBranch, Base: m.TargetBranch, HeadSHA: m.SHA, Draft: m.Draft || m.WorkInProgress,
		UpdatedAt: parseTime(m.UpdatedAt), RequestedReviewers: []model.HostUser{},
	}
	for _, r := range m.Reviewers {
		s.RequestedReviewers = append(s.RequestedReviewers, r.host())
	}
	return s
}

func glProject(repo string) string { return "/projects/" + url.PathEscape(repo) }

// glNext builds the next page target from X-Next-Page.
func glNext(cur string, h http.Header) string {
	np := strings.TrimSpace(h.Get("X-Next-Page"))
	if np == "" {
		return ""
	}
	u, err := url.Parse(cur)
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("page", np)
	u.RawQuery = q.Encode()
	return u.String()
}

func (g *GitLab) Me(ctx context.Context) (model.HostUser, error) {
	var u glUser
	if err := g.api.do(ctx, http.MethodGet, "/user", nil, &u); err != nil {
		return model.HostUser{}, err
	}
	return u.host(), nil
}

// TokenInfo reads /personal_access_tokens/self; OAuth and other tokens that
// endpoint rejects fall back to /user, with write unknown.
func (g *GitLab) TokenInfo(ctx context.Context) (model.TokenInfo, error) {
	var t struct {
		Scopes    []string `json:"scopes"`
		ExpiresAt string   `json:"expires_at"`
	}
	err := g.api.do(ctx, http.MethodGet, "/personal_access_tokens/self", nil, &t)
	var pe *model.ProviderError
	if errors.As(err, &pe) && pe.Status != 401 && !pe.RateLimited {
		if _, err := g.Me(ctx); err != nil {
			return model.TokenInfo{}, err
		}
		return model.TokenInfo{Scopes: []string{}, CanRead: true, CanWrite: model.TriUnknown}, nil
	}
	if err != nil {
		return model.TokenInfo{}, err
	}
	info := model.TokenInfo{Scopes: append([]string{}, t.Scopes...), CanWrite: model.TriNo}
	for _, s := range t.Scopes {
		switch s {
		case "api":
			info.CanRead, info.CanWrite = true, model.TriYes
		case "read_api":
			info.CanRead = true
		}
	}
	if d, err := time.Parse("2006-01-02", t.ExpiresAt); err == nil {
		info.ExpiresAt = &d
	}
	return info, nil
}

func (g *GitLab) ListPullRequests(ctx context.Context, repo string, f model.PRFilter) ([]model.PRSummary, error) {
	base := glProject(repo) + "/merge_requests?state=opened&order_by=updated_at&sort=desc&per_page=100"
	var queries []string
	if !isZeroUser(f.Author) {
		queries = append(queries, base+"&"+glUserParam("author", f.Author))
	}
	if !isZeroUser(f.Reviewer) {
		queries = append(queries, base+"&"+glUserParam("reviewer", f.Reviewer))
	}
	if len(queries) == 0 {
		queries = []string{base}
	}
	seen := map[int]bool{}
	var out []model.PRSummary
	for _, q := range queries {
		page, err := pageAll(ctx, g.api, q, false, maxListPRs, func(cur string, r response) ([]model.PRSummary, string, error) {
			var raw []glMRFull
			if err := json.Unmarshal(r.body, &raw); err != nil {
				return nil, "", fmt.Errorf("gitlab API returned unexpected JSON: %w", err)
			}
			s := make([]model.PRSummary, 0, len(raw))
			for _, m := range raw {
				s = append(s, m.summary())
			}
			return s, glNext(cur, r.header), nil
		})
		if err != nil {
			return nil, err
		}
		for _, p := range page {
			if !seen[p.Number] {
				seen[p.Number] = true
				out = append(out, p)
			}
		}
	}
	if len(queries) > 1 {
		sortByUpdated(out)
	}
	if len(out) > maxListPRs {
		out = out[:maxListPRs]
	}
	return out, nil
}

func glUserParam(kind string, u model.HostUser) string {
	if u.Login != "" {
		return kind + "_username=" + url.QueryEscape(u.Login)
	}
	return kind + "_id=" + url.QueryEscape(u.ID)
}

func (g *GitLab) GetPullRequest(ctx context.Context, repo string, number int) (model.PRDetail, error) {
	var m glMRFull
	mr := fmt.Sprintf("%s/merge_requests/%d", glProject(repo), number)
	if err := g.api.do(ctx, http.MethodGet, mr, nil, &m); err != nil {
		return model.PRDetail{}, err
	}
	d := model.PRDetail{PRSummary: m.summary(), Body: m.Description, SourceRepo: repo, CI: glCI(m)}
	d.ChangedFiles, _ = strconv.Atoi(strings.TrimRight(m.ChangesCount, "+"))
	if m.DiffRefs != nil {
		d.BaseSHA, d.StartSHA = m.DiffRefs.BaseSHA, m.DiffRefs.StartSHA
		if m.DiffRefs.HeadSHA != "" {
			d.HeadSHA = m.DiffRefs.HeadSHA
		}
	}
	if m.SourceProjectID != 0 && m.SourceProjectID != m.TargetProjectID {
		var p struct {
			Path string `json:"path_with_namespace"`
		}
		d.SourceRepo = fmt.Sprintf("project:%d", m.SourceProjectID)
		if err := g.api.do(ctx, http.MethodGet, fmt.Sprintf("/projects/%d", m.SourceProjectID), nil, &p); err == nil && p.Path != "" {
			d.SourceRepo = p.Path
		}
	}
	me, err := g.Me(ctx)
	if err != nil {
		return model.PRDetail{}, err
	}
	var ap struct {
		ApprovedBy []struct {
			User glUser `json:"user"`
		} `json:"approved_by"`
	}
	err = g.api.do(ctx, http.MethodGet, mr+"/approvals", nil, &ap)
	var pe *model.ProviderError
	if err != nil && !(errors.As(err, &pe) && (pe.Status == 403 || pe.Status == 404)) {
		return model.PRDetail{}, err
	}
	for _, a := range ap.ApprovedBy {
		if sameUser(me, a.User.host()) {
			d.MyLatestReview = &model.HostReview{Verdict: model.VerdictApprove}
		}
	}
	return d, nil
}

func glCI(m glMRFull) string {
	if m.HeadPipeline == nil {
		return "none"
	}
	switch m.HeadPipeline.Status {
	case "success", "success-with-warnings":
		return "passing"
	case "failed", "canceled":
		return "failing"
	case "running", "pending", "created", "preparing", "waiting_for_resource", "scheduled":
		return "running"
	}
	return "none"
}

type glDiff struct {
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	NewFile     bool   `json:"new_file"`
	RenamedFile bool   `json:"renamed_file"`
	DeletedFile bool   `json:"deleted_file"`
	Diff        string `json:"diff"`
	Collapsed   bool   `json:"collapsed"`
	TooLarge    bool   `json:"too_large"`
}

// PullRequestFiles reads /diffs (GitLab 15.7+) and falls back to the older /changes.
func (g *GitLab) PullRequestFiles(ctx context.Context, repo string, number int) ([]model.PRFile, error) {
	mr := fmt.Sprintf("%s/merge_requests/%d", glProject(repo), number)
	diffs, err := pageAll(ctx, g.api, mr+"/diffs?per_page=100", false, 0, func(cur string, r response) ([]glDiff, string, error) {
		var raw []glDiff
		if err := json.Unmarshal(r.body, &raw); err != nil {
			return nil, "", fmt.Errorf("gitlab API returned unexpected JSON: %w", err)
		}
		return raw, glNext(cur, r.header), nil
	})
	var pe *model.ProviderError
	if errors.As(err, &pe) && pe.Status == 404 {
		var ch struct {
			Changes []glDiff `json:"changes"`
		}
		if err := g.api.do(ctx, http.MethodGet, mr+"/changes", nil, &ch); err != nil {
			return nil, err
		}
		diffs, err = ch.Changes, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]model.PRFile, 0, len(diffs))
	for _, d := range diffs {
		status := "modified"
		switch {
		case d.NewFile:
			status = "added"
		case d.DeletedFile:
			status = "deleted"
		case d.RenamedFile:
			status = "renamed"
		}
		add, del := countPatch(d.Diff)
		out = append(out, model.PRFile{
			Path: d.NewPath, OldPath: d.OldPath, Status: status, Additions: add, Deletions: del,
			Patch: withHeader(d.Diff, d.OldPath, d.NewPath, status), Truncated: d.Collapsed || d.TooLarge,
		})
	}
	return capFiles(out), nil
}

// PostReview creates a draft note per comment (plus one for the summary) and
// publishes them together, then approves. Request-changes has no REST
// equivalent and is refused before anything is posted. A comment GitLab
// rejects lands in Failed; the rest are still published.
func (g *GitLab) PostReview(ctx context.Context, repo string, number int, r model.ReviewSubmission) (model.PostedReview, error) {
	if r.Verdict == model.VerdictRequestChanges {
		return model.PostedReview{}, &model.ProviderError{Provider: "gitlab", Status: 422, Message: "GitLab has no API to request changes; use comment or approve"}
	}
	mr := fmt.Sprintf("%s/merge_requests/%d", glProject(repo), number)
	var m glMRFull
	if err := g.api.do(ctx, http.MethodGet, mr, nil, &m); err != nil {
		return model.PostedReview{}, err
	}
	if m.DiffRefs == nil || (r.HeadSHA != "" && m.DiffRefs.HeadSHA != r.HeadSHA) {
		return model.PostedReview{}, &model.ProviderError{Provider: "gitlab", Status: 409, Message: "merge request head moved since the review was made"}
	}
	refs := m.DiffRefs
	out := model.PostedReview{Ref: fmt.Sprintf("gitlab:%s!%d", repo, number), Comments: map[string]string{}, Failed: map[string]string{}}
	drafts := map[string]string{} // draft id → GitLab draft note id
	var created []string
	create := func(body map[string]any) (string, error) {
		var n struct {
			ID int64 `json:"id"`
		}
		if err := g.api.do(ctx, http.MethodPost, mr+"/draft_notes", body, &n); err != nil {
			return "", err
		}
		id := strconv.FormatInt(n.ID, 10)
		created = append(created, id)
		return id, nil
	}
	cleanup := func() {
		for _, id := range created {
			_ = g.api.do(context.WithoutCancel(ctx), http.MethodDelete, mr+"/draft_notes/"+id, nil, nil)
		}
	}
	for _, c := range r.Comments {
		if c.PostedRef != "" {
			out.Comments[c.DraftID] = c.PostedRef
			continue
		}
		if err := ctx.Err(); err != nil {
			cleanup()
			return model.PostedReview{}, err
		}
		oldPath := c.OldPath
		if oldPath == "" {
			oldPath = c.Path
		}
		pos := map[string]any{
			"position_type": "text", "base_sha": refs.BaseSHA, "start_sha": refs.StartSHA, "head_sha": refs.HeadSHA,
			"old_path": oldPath, "new_path": c.Path,
		}
		if c.Side == "old" {
			pos["old_line"] = c.Line
		} else {
			pos["new_line"] = c.Line
			if c.OldLine > 0 {
				pos["old_line"] = c.OldLine
			}
		}
		id, err := create(map[string]any{"note": c.Body, "position": pos})
		if err != nil {
			var pe *model.ProviderError
			if errors.As(err, &pe) && pe.RateLimited || ctx.Err() != nil {
				cleanup()
				return model.PostedReview{}, err
			}
			out.Failed[c.DraftID] = err.Error()
			continue
		}
		drafts[c.DraftID] = id
	}
	if strings.TrimSpace(r.Body) != "" {
		if _, err := create(map[string]any{"note": r.Body}); err != nil {
			cleanup()
			return model.PostedReview{}, err
		}
	}
	if len(created) > 0 {
		if err := g.api.do(ctx, http.MethodPost, mr+"/draft_notes/bulk_publish", nil, nil); err != nil {
			cleanup()
			return model.PostedReview{}, err
		}
	}
	for draft, id := range drafts {
		out.Comments[draft] = id
	}
	if r.Verdict == model.VerdictApprove {
		if err := g.api.do(ctx, http.MethodPost, mr+"/approve", map[string]string{"sha": refs.HeadSHA}, nil); err != nil {
			out.VerdictErr = err.Error()
		}
	}
	return out, nil
}
