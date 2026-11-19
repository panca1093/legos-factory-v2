package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"legos-factory/backend/internal/model"
)

type ghUser struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

func (u ghUser) host() model.HostUser {
	h := model.HostUser{Login: u.Login}
	if u.ID != 0 {
		h.ID = strconv.FormatInt(u.ID, 10)
	}
	return h
}

type ghPR struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	HTMLURL   string `json:"html_url"`
	Body      string `json:"body"`
	Draft     bool   `json:"draft"`
	UpdatedAt string `json:"updated_at"`
	User      ghUser `json:"user"`
	Head      struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
	RequestedReviewers []ghUser `json:"requested_reviewers"`
	Additions          int      `json:"additions"`
	Deletions          int      `json:"deletions"`
	ChangedFiles       int      `json:"changed_files"`
}

func (p ghPR) summary() model.PRSummary {
	s := model.PRSummary{
		Number: p.Number, Title: p.Title, URL: p.HTMLURL, Author: p.User.host(),
		Head: p.Head.Ref, Base: p.Base.Ref, HeadSHA: p.Head.SHA, Draft: p.Draft,
		UpdatedAt: parseTime(p.UpdatedAt), RequestedReviewers: []model.HostUser{},
	}
	for _, r := range p.RequestedReviewers {
		s.RequestedReviewers = append(s.RequestedReviewers, r.host())
	}
	return s
}

func (g *GitHub) Me(ctx context.Context) (model.HostUser, error) {
	g.mu.Lock()
	me := g.me
	g.mu.Unlock()
	if me != nil {
		return *me, nil
	}
	var u ghUser
	if _, err := g.api.call(ctx, http.MethodGet, "/user", nil, &u, reqOpts{cache: true}); err != nil {
		return model.HostUser{}, err
	}
	h := u.host()
	g.mu.Lock()
	g.me = &h
	g.mu.Unlock()
	return h, nil
}

// TokenInfo reads X-OAuth-Scopes (classic and OAuth tokens) and the expiry
// header; fine-grained tokens send no scopes header, so write is unknown.
func (g *GitHub) TokenInfo(ctx context.Context) (model.TokenInfo, error) {
	h, err := g.api.call(ctx, http.MethodGet, "/user", nil, &struct{}{}, reqOpts{})
	if err != nil {
		return model.TokenInfo{}, err
	}
	info := model.TokenInfo{CanRead: true, CanWrite: model.TriUnknown, Scopes: []string{}}
	if vals, ok := h[http.CanonicalHeaderKey("X-OAuth-Scopes")]; ok {
		has := map[string]bool{}
		for _, s := range strings.Split(strings.Join(vals, ","), ",") {
			if s = strings.TrimSpace(s); s != "" {
				info.Scopes = append(info.Scopes, s)
				has[s] = true
			}
		}
		switch {
		case has["repo"]:
			info.CanWrite = model.TriYes
		case has["public_repo"]:
			info.CanWrite = model.TriUnknown
		default:
			info.CanWrite = model.TriNo
		}
	}
	if v := strings.TrimSpace(h.Get("github-authentication-token-expiration")); v != "" {
		for _, layout := range []string{"2006-01-02 15:04:05 MST", "2006-01-02 15:04:05 -0700", time.RFC3339} {
			if t, err := time.Parse(layout, v); err == nil {
				info.ExpiresAt = &t
				break
			}
		}
	}
	return info, nil
}

func (g *GitHub) ListPullRequests(ctx context.Context, repo string, f model.PRFilter) ([]model.PRSummary, error) {
	if err := checkRepo("github", repo); err != nil {
		return nil, err
	}
	first := "/repos/" + repo + "/pulls?state=open&sort=updated&direction=desc&per_page=100"
	return pageAll(ctx, g.api, first, true, maxListPRs, func(_ string, r response) ([]model.PRSummary, string, error) {
		var raw []ghPR
		if err := json.Unmarshal(r.body, &raw); err != nil {
			return nil, "", fmt.Errorf("github API returned unexpected JSON: %w", err)
		}
		var out []model.PRSummary
		for _, p := range raw {
			if s := p.summary(); matchesFilter(f, s.Author, s.RequestedReviewers) {
				out = append(out, s)
			}
		}
		return out, nextLink(r.header), nil
	})
}

func (g *GitHub) GetPullRequest(ctx context.Context, repo string, number int) (model.PRDetail, error) {
	if err := checkRepo("github", repo); err != nil {
		return model.PRDetail{}, err
	}
	var p ghPR
	path := fmt.Sprintf("/repos/%s/pulls/%d", repo, number)
	if _, err := g.api.call(ctx, http.MethodGet, path, nil, &p, reqOpts{cache: true}); err != nil {
		return model.PRDetail{}, err
	}
	d := model.PRDetail{
		PRSummary: p.summary(), BaseSHA: p.Base.SHA, StartSHA: p.Base.SHA, Body: p.Body,
		Additions: p.Additions, Deletions: p.Deletions, ChangedFiles: p.ChangedFiles,
	}
	if p.Head.Repo != nil {
		d.SourceRepo = p.Head.Repo.FullName
	}
	var err error
	if d.CI, err = g.ci(ctx, repo, p.Head.SHA); err != nil {
		return model.PRDetail{}, err
	}
	me, err := g.Me(ctx)
	if err != nil {
		return model.PRDetail{}, err
	}
	d.MyLatestReview, err = g.latestReview(ctx, repo, number, me)
	return d, err
}

// ci folds check runs and the legacy combined status; a failure to read them
// (fine-grained tokens without checks:read) degrades to "none".
func (g *GitHub) ci(ctx context.Context, repo, sha string) (string, error) {
	var c ciCount
	var runs struct {
		CheckRuns []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if _, err := g.api.call(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100", repo, sha), nil, &runs, reqOpts{cache: true}); err == nil {
		for _, r := range runs.CheckRuns {
			switch {
			case r.Status != "completed":
				c.running++
			case r.Conclusion == "failure" || r.Conclusion == "timed_out" || r.Conclusion == "action_required" || r.Conclusion == "startup_failure":
				c.failing++
			case r.Conclusion == "success" || r.Conclusion == "neutral" || r.Conclusion == "skipped":
				c.passing++
			}
		}
	}
	var st struct {
		State      string `json:"state"`
		TotalCount int    `json:"total_count"`
	}
	if _, err := g.api.call(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/commits/%s/status", repo, sha), nil, &st, reqOpts{cache: true}); err == nil && st.TotalCount > 0 {
		switch st.State {
		case "failure", "error":
			c.failing++
		case "pending":
			c.running++
		case "success":
			c.passing++
		}
	}
	return c.rollup(), ctx.Err()
}

func (g *GitHub) latestReview(ctx context.Context, repo string, number int, me model.HostUser) (*model.HostReview, error) {
	type review struct {
		User     ghUser `json:"user"`
		State    string `json:"state"`
		CommitID string `json:"commit_id"`
	}
	first := fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", repo, number)
	all, err := pageAll(ctx, g.api, first, true, 0, func(_ string, r response) ([]review, string, error) {
		var raw []review
		if err := json.Unmarshal(r.body, &raw); err != nil {
			return nil, "", fmt.Errorf("github API returned unexpected JSON: %w", err)
		}
		return raw, nextLink(r.header), nil
	})
	if err != nil {
		return nil, err
	}
	var latest *model.HostReview
	for _, r := range all {
		if !sameUser(me, r.User.host()) {
			continue
		}
		switch r.State {
		case "APPROVED":
			latest = &model.HostReview{Verdict: model.VerdictApprove, CommitID: r.CommitID}
		case "CHANGES_REQUESTED":
			latest = &model.HostReview{Verdict: model.VerdictRequestChanges, CommitID: r.CommitID}
		case "COMMENTED":
			latest = &model.HostReview{Verdict: model.VerdictComment, CommitID: r.CommitID}
		}
	}
	return latest, nil
}

func (g *GitHub) PullRequestFiles(ctx context.Context, repo string, number int) ([]model.PRFile, error) {
	if err := checkRepo("github", repo); err != nil {
		return nil, err
	}
	type file struct {
		Filename         string `json:"filename"`
		PreviousFilename string `json:"previous_filename"`
		Status           string `json:"status"`
		Additions        int    `json:"additions"`
		Deletions        int    `json:"deletions"`
		Patch            string `json:"patch"`
	}
	first := fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100", repo, number)
	raw, err := pageAll(ctx, g.api, first, true, 0, func(_ string, r response) ([]file, string, error) {
		var fs []file
		if err := json.Unmarshal(r.body, &fs); err != nil {
			return nil, "", fmt.Errorf("github API returned unexpected JSON: %w", err)
		}
		return fs, nextLink(r.header), nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]model.PRFile, 0, len(raw))
	for _, f := range raw {
		status, old := "modified", f.Filename
		switch f.Status {
		case "added", "copied":
			status = "added"
		case "removed":
			status = "deleted"
		case "renamed":
			status = "renamed"
		}
		if f.PreviousFilename != "" {
			old = f.PreviousFilename
		}
		out = append(out, model.PRFile{
			Path: f.Filename, OldPath: old, Status: status, Additions: f.Additions, Deletions: f.Deletions,
			Patch: withHeader(f.Patch, old, f.Filename, status),
		})
	}
	return capFiles(out), nil
}

// PostReview submits one atomic review: GitHub rejects the whole request
// (422) when any comment line is outside the diff, so the usecase must
// pre-validate anchors. On error nothing was posted.
func (g *GitHub) PostReview(ctx context.Context, repo string, number int, r model.ReviewSubmission) (model.PostedReview, error) {
	if err := checkRepo("github", repo); err != nil {
		return model.PostedReview{}, err
	}
	type comment struct {
		Path string `json:"path"`
		Line int    `json:"line"`
		Side string `json:"side"`
		Body string `json:"body"`
	}
	event := map[string]string{model.VerdictApprove: "APPROVE", model.VerdictRequestChanges: "REQUEST_CHANGES"}[r.Verdict]
	if event == "" {
		event = "COMMENT"
	}
	comments := make([]comment, 0, len(r.Comments))
	for _, c := range r.Comments {
		side := "RIGHT"
		if c.Side == "old" {
			side = "LEFT"
		}
		comments = append(comments, comment{Path: c.Path, Line: c.Line, Side: side, Body: c.Body})
	}
	var resp struct {
		ID int64 `json:"id"`
	}
	err := g.api.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/pulls/%d/reviews", repo, number), map[string]any{
		"commit_id": r.HeadSHA, "body": r.Body, "event": event, "comments": comments,
	}, &resp)
	if err != nil {
		return model.PostedReview{}, err
	}
	ref := strconv.FormatInt(resp.ID, 10)
	out := model.PostedReview{Ref: ref, Comments: map[string]string{}, Failed: map[string]string{}}
	for _, c := range r.Comments {
		out.Comments[c.DraftID] = ref
	}
	return out, nil
}
