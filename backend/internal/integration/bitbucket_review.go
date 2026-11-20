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

	"legos-factory/backend/internal/model"
)

const bbDiffLimit = 4 << 20

type bbUser struct {
	UUID        string `json:"uuid"`
	Username    string `json:"username"`
	Nickname    string `json:"nickname"`
	DisplayName string `json:"display_name"`
}

func (u bbUser) host() model.HostUser {
	login := u.Username
	if login == "" {
		login = u.Nickname
	}
	if login == "" {
		login = u.DisplayName
	}
	return model.HostUser{Login: login, ID: u.UUID}
}

type bbPRFull struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Draft       bool   `json:"draft"`
	UpdatedOn   string `json:"updated_on"`
	Author      bbUser `json:"author"`
	Links       struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
	Source struct {
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
		Commit struct {
			Hash string `json:"hash"`
		} `json:"commit"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	} `json:"source"`
	Destination struct {
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
		Commit struct {
			Hash string `json:"hash"`
		} `json:"commit"`
	} `json:"destination"`
	Reviewers    []bbUser `json:"reviewers"`
	Participants []struct {
		User     bbUser `json:"user"`
		Role     string `json:"role"`
		Approved bool   `json:"approved"`
		State    string `json:"state"`
	} `json:"participants"`
}

// reviewers merges the reviewers list with participants of role REVIEWER.
func (p bbPRFull) reviewers() []model.HostUser {
	out := []model.HostUser{}
	for _, r := range p.Reviewers {
		out = append(out, r.host())
	}
	for _, part := range p.Participants {
		if part.Role == "REVIEWER" {
			out = append(out, part.User.host())
		}
	}
	return out
}

// summary: Bitbucket reports a 12-character commit hash here; GetPullRequest resolves the full SHA.
func (p bbPRFull) summary() model.PRSummary {
	return model.PRSummary{
		Number: p.ID, Title: p.Title, URL: p.Links.HTML.Href, Author: p.Author.host(),
		Head: p.Source.Branch.Name, Base: p.Destination.Branch.Name, HeadSHA: p.Source.Commit.Hash, Draft: p.Draft,
		UpdatedAt: parseTime(p.UpdatedOn), RequestedReviewers: p.reviewers(),
	}
}

func (b *Bitbucket) Me(ctx context.Context) (model.HostUser, error) {
	var u bbUser
	if err := b.api.do(ctx, http.MethodGet, "/user", nil, &u); err != nil {
		return model.HostUser{}, err
	}
	return u.host(), nil
}

// TokenInfo probes read with GET /user. Write stays unknown unless the host
// sends X-OAuth-Scopes (TODO: uncertain — whether Atlassian API tokens send it, verify against a real workspace before prod).
func (b *Bitbucket) TokenInfo(ctx context.Context) (model.TokenInfo, error) {
	h, err := b.api.call(ctx, http.MethodGet, "/user", nil, &struct{}{}, reqOpts{})
	if err != nil {
		return model.TokenInfo{}, err
	}
	info := model.TokenInfo{Scopes: []string{}, CanRead: true, CanWrite: model.TriUnknown}
	if v := strings.TrimSpace(h.Get("X-OAuth-Scopes")); v != "" {
		write := false
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				info.Scopes = append(info.Scopes, s)
				write = write || s == "pullrequest:write" || s == "pullrequest"
			}
		}
		info.CanWrite = model.TriNo
		if write {
			info.CanWrite = model.TriYes
		}
	}
	return info, nil
}

func bbRepo(repo string) string { return "/repositories/" + repo }

func bbPage[T any](ctx context.Context, b *Bitbucket, first string, maxItems int, keep func(T) bool) ([]T, error) {
	return pageAll(ctx, b.api, first, false, maxItems, func(_ string, r response) ([]T, string, error) {
		var page struct {
			Values []T    `json:"values"`
			Next   string `json:"next"`
		}
		if err := json.Unmarshal(r.body, &page); err != nil {
			return nil, "", fmt.Errorf("bitbucket API returned unexpected JSON: %w", err)
		}
		var out []T
		for _, v := range page.Values {
			if keep == nil || keep(v) {
				out = append(out, v)
			}
		}
		next := page.Next
		if b.proxy {
			next, _ = strings.CutPrefix(next, "https://api.bitbucket.org/2.0")
		}
		return out, next, nil
	})
}

// ListPullRequests asks BBQL (author.uuid / reviewers.uuid) and, when the
// host rejects the query, lists all open PRs and filters locally.
func (b *Bitbucket) ListPullRequests(ctx context.Context, repo string, f model.PRFilter) ([]model.PRSummary, error) {
	if err := checkRepo("bitbucket", repo); err != nil {
		return nil, err
	}
	base := bbRepo(repo) + "/pullrequests?state=OPEN&pagelen=50&sort=-updated_on"
	local := (!isZeroUser(f.Author) && f.Author.ID == "") || (!isZeroUser(f.Reviewer) && f.Reviewer.ID == "")
	var all []bbPRFull
	if !local {
		queries := []string{}
		if !isZeroUser(f.Author) {
			queries = append(queries, `author.uuid="`+f.Author.ID+`"`)
		}
		if !isZeroUser(f.Reviewer) {
			queries = append(queries, `reviewers.uuid="`+f.Reviewer.ID+`"`)
		}
		if len(queries) == 0 {
			queries = []string{""}
		}
		for _, q := range queries {
			target := base
			if q != "" {
				target += "&q=" + url.QueryEscape(`state="OPEN" AND `+q)
			}
			page, err := bbPage[bbPRFull](ctx, b, target, maxListPRs, nil)
			var pe *model.ProviderError
			if errors.As(err, &pe) && pe.Status == 400 {
				local, all = true, nil
				break
			}
			if err != nil {
				return nil, err
			}
			all = append(all, page...)
		}
	}
	if local {
		var err error
		all, err = bbPage(ctx, b, base, maxListPRs, func(p bbPRFull) bool {
			return matchesFilter(f, p.Author.host(), p.reviewers())
		})
		if err != nil {
			return nil, err
		}
	}
	seen := map[int]bool{}
	out := []model.PRSummary{}
	for _, p := range all {
		if !seen[p.ID] {
			seen[p.ID] = true
			out = append(out, p.summary())
		}
	}
	sortByUpdated(out)
	if len(out) > maxListPRs {
		out = out[:maxListPRs]
	}
	return out, nil
}

func (b *Bitbucket) fullHash(ctx context.Context, repo, hash string) string {
	var c struct {
		Hash string `json:"hash"`
	}
	if hash == "" || repo == "" || checkRepo("bitbucket", repo) != nil {
		return hash
	}
	if err := b.api.do(ctx, http.MethodGet, bbRepo(repo)+"/commit/"+url.PathEscape(hash), nil, &c); err != nil || c.Hash == "" {
		return hash
	}
	return c.Hash
}

func (b *Bitbucket) GetPullRequest(ctx context.Context, repo string, number int) (model.PRDetail, error) {
	if err := checkRepo("bitbucket", repo); err != nil {
		return model.PRDetail{}, err
	}
	var p bbPRFull
	pr := fmt.Sprintf("%s/pullrequests/%d", bbRepo(repo), number)
	if err := b.api.do(ctx, http.MethodGet, pr, nil, &p); err != nil {
		return model.PRDetail{}, err
	}
	d := model.PRDetail{PRSummary: p.summary(), Body: p.Description, SourceRepo: p.Source.Repository.FullName}
	d.HeadSHA = b.fullHash(ctx, p.Source.Repository.FullName, p.Source.Commit.Hash)
	d.BaseSHA = b.fullHash(ctx, repo, p.Destination.Commit.Hash)
	d.StartSHA = d.BaseSHA

	stats, err := b.diffstat(ctx, repo, number)
	if err != nil {
		return model.PRDetail{}, err
	}
	d.ChangedFiles = len(stats)
	for _, s := range stats {
		d.Additions += s.LinesAdded
		d.Deletions += s.LinesRemoved
	}

	var c ciCount
	statuses, err := bbPage[struct {
		State string `json:"state"`
	}](ctx, b, pr+"/statuses?pagelen=100", 0, nil)
	if err != nil && ctx.Err() != nil {
		return model.PRDetail{}, ctx.Err()
	}
	for _, s := range statuses {
		switch s.State {
		case "SUCCESSFUL":
			c.passing++
		case "FAILED":
			c.failing++
		case "INPROGRESS":
			c.running++
		}
	}
	d.CI = c.rollup()

	me, err := b.Me(ctx)
	if err != nil {
		return model.PRDetail{}, err
	}
	for _, part := range p.Participants {
		if !sameUser(me, part.User.host()) {
			continue
		}
		switch {
		case part.State == "changes_requested":
			d.MyLatestReview = &model.HostReview{Verdict: model.VerdictRequestChanges}
		case part.Approved || part.State == "approved":
			d.MyLatestReview = &model.HostReview{Verdict: model.VerdictApprove}
		}
	}
	return d, nil
}

type bbStat struct {
	Status       string `json:"status"`
	LinesAdded   int    `json:"lines_added"`
	LinesRemoved int    `json:"lines_removed"`
	Old          *struct {
		Path string `json:"path"`
	} `json:"old"`
	New *struct {
		Path string `json:"path"`
	} `json:"new"`
}

func (b *Bitbucket) diffstat(ctx context.Context, repo string, number int) ([]bbStat, error) {
	return bbPage[bbStat](ctx, b, fmt.Sprintf("%s/pullrequests/%d/diffstat?pagelen=100", bbRepo(repo), number), 0, nil)
}

func (b *Bitbucket) PullRequestFiles(ctx context.Context, repo string, number int) ([]model.PRFile, error) {
	if err := checkRepo("bitbucket", repo); err != nil {
		return nil, err
	}
	stats, err := b.diffstat(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	r, err := b.api.send(ctx, http.MethodGet, fmt.Sprintf("%s/pullrequests/%d/diff", bbRepo(repo), number), nil,
		reqOpts{limit: bbDiffLimit, accept: "text/plain"})
	if err != nil {
		return nil, err
	}
	raw := string(r.body)
	if r.capped {
		if i := strings.LastIndex(raw, "\ndiff --git "); i >= 0 {
			raw = raw[:i+1]
		} else {
			raw = ""
		}
	}
	patches := splitDiff(raw)
	out := make([]model.PRFile, 0, len(stats))
	for _, s := range stats {
		f := model.PRFile{Additions: s.LinesAdded, Deletions: s.LinesRemoved}
		if s.New != nil {
			f.Path = s.New.Path
		}
		if s.Old != nil {
			f.OldPath = s.Old.Path
		}
		if f.Path == "" {
			f.Path = f.OldPath
		}
		if f.OldPath == "" {
			f.OldPath = f.Path
		}
		switch s.Status {
		case "added":
			f.Status = "added"
		case "removed":
			f.Status = "deleted"
		case "renamed":
			f.Status = "renamed"
		default:
			f.Status = "modified"
		}
		f.Patch = patches[f.Path]
		if f.Patch == "" {
			f.Patch = patches[f.OldPath]
		}
		out = append(out, f)
	}
	return capFiles(out), nil
}

// splitDiff maps each file of a git-style diff to its text from the ---
// line on. Binary and rename-only blocks have no ---/+++ lines and are skipped.
func splitDiff(raw string) map[string]string {
	out := map[string]string{}
	for _, block := range strings.Split("\n"+raw, "\ndiff --git ")[1:] {
		i := strings.Index("\n"+block, "\n--- ")
		if i < 0 {
			continue
		}
		patch := ("\n" + block)[i+1:]
		lines := strings.SplitN(patch, "\n", 3)
		if len(lines) < 3 || !strings.HasPrefix(lines[1], "+++ ") {
			continue
		}
		path := diffPath(lines[1][4:], "b/")
		if path == "" {
			path = diffPath(lines[0][4:], "a/")
		}
		if path != "" {
			out[path] = strings.TrimRight(patch, "\n") + "\n"
		}
	}
	return out
}

func diffPath(s, prefix string) string {
	s, _, _ = strings.Cut(s, "\t")
	if s == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(s, prefix)
}

// PostReview posts inline comments one by one (Bitbucket has no batched
// review), then the summary comment, then the verdict. A comment with
// PostedRef is skipped, so a retry never duplicates it. Per-comment failures
// go to Failed (the summary under "summary"), a verdict failure to VerdictErr.
func (b *Bitbucket) PostReview(ctx context.Context, repo string, number int, r model.ReviewSubmission) (model.PostedReview, error) {
	if err := checkRepo("bitbucket", repo); err != nil {
		return model.PostedReview{}, err
	}
	pr := fmt.Sprintf("%s/pullrequests/%d", bbRepo(repo), number)
	out := model.PostedReview{Comments: map[string]string{}, Failed: map[string]string{}}
	post := func(body map[string]any) (string, error) {
		var c struct {
			ID int64 `json:"id"`
		}
		if err := b.api.do(ctx, http.MethodPost, pr+"/comments", body, &c); err != nil {
			return "", err
		}
		return strconv.FormatInt(c.ID, 10), nil
	}
	for _, c := range r.Comments {
		if c.PostedRef != "" {
			out.Comments[c.DraftID] = c.PostedRef
			continue
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
		inline := map[string]any{"path": c.Path, "to": c.Line}
		if c.Side == "old" {
			inline = map[string]any{"path": c.OldPath, "from": c.Line}
			if c.OldPath == "" {
				inline["path"] = c.Path
			}
		}
		id, err := post(map[string]any{"content": map[string]string{"raw": c.Body}, "inline": inline})
		if err != nil {
			var pe *model.ProviderError
			if errors.As(err, &pe) && pe.RateLimited || ctx.Err() != nil {
				return out, err
			}
			out.Failed[c.DraftID] = err.Error()
			continue
		}
		out.Comments[c.DraftID] = id
		if out.Ref == "" {
			out.Ref = id
		}
	}
	if strings.TrimSpace(r.Body) != "" {
		id, err := post(map[string]any{"content": map[string]string{"raw": r.Body}})
		if err != nil {
			out.Failed["summary"] = err.Error()
		} else {
			out.Ref = id
		}
	}
	action := map[string]string{model.VerdictApprove: "/approve", model.VerdictRequestChanges: "/request-changes"}[r.Verdict]
	if action != "" {
		if err := b.api.do(ctx, http.MethodPost, pr+action, nil, nil); err != nil {
			out.VerdictErr = err.Error()
		}
	}
	return out, nil
}
