package integration

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"legos-factory/backend/internal/model"
)

const (
	maxListPRs     = 50      // list fan-out cap (SPEC §Errors and rate limits)
	maxReviewFiles = 300     // files beyond this are listed without a patch
	maxPatchBytes  = 1 << 20 // total patch text kept per pull request
	maxPages       = 30
)

var repoSlug = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// checkRepo guards owner/name hosts (GitHub, Bitbucket) against path injection.
func checkRepo(provider, repo string) error {
	owner, name, _ := strings.Cut(repo, "/")
	if !repoSlug.MatchString(repo) || owner == ".." || name == ".." || owner == "." || name == "." {
		return fmt.Errorf("%w: %s repo must be owner/name, got %q", model.ErrPermanent, provider, repo)
	}
	return nil
}

// pageAll follows pages starting at first. decode returns the page's items
// (already filtered) and the next target, "" at the end. maxItems 0 = no cap.
func pageAll[T any](ctx context.Context, a api, first string, cache bool, maxItems int,
	decode func(cur string, r response) ([]T, string, error)) ([]T, error) {
	var all []T
	cur := first
	for page := 0; cur != "" && page < maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r, err := a.send(ctx, http.MethodGet, cur, nil, reqOpts{cache: cache})
		if err != nil {
			return nil, err
		}
		items, next, err := decode(cur, r)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if maxItems > 0 && len(all) >= maxItems {
			return all[:maxItems], nil
		}
		cur = next
	}
	return all, nil
}

// nextLink returns the rel="next" target of an RFC 8288 Link header.
func nextLink(h http.Header) string {
	for _, part := range strings.Split(h.Get("Link"), ",") {
		target, params, ok := strings.Cut(part, ";")
		if ok && strings.Contains(params, `rel="next"`) {
			return strings.Trim(strings.TrimSpace(target), "<>")
		}
	}
	return ""
}

func sameUser(a, b model.HostUser) bool {
	if a.ID != "" && b.ID != "" {
		return a.ID == b.ID
	}
	return a.Login != "" && strings.EqualFold(a.Login, b.Login)
}

func isZeroUser(u model.HostUser) bool { return u.Login == "" && u.ID == "" }

// matchesFilter is true when the PR is by the filter's author or awaits its
// reviewer; both set means either (the "related" view); neither set matches all.
func matchesFilter(f model.PRFilter, author model.HostUser, reviewers []model.HostUser) bool {
	if isZeroUser(f.Author) && isZeroUser(f.Reviewer) {
		return true
	}
	if !isZeroUser(f.Author) && sameUser(f.Author, author) {
		return true
	}
	if !isZeroUser(f.Reviewer) {
		for _, r := range reviewers {
			if sameUser(f.Reviewer, r) {
				return true
			}
		}
	}
	return false
}

// ciCount rolls individual check results into the PRDetail.CI value.
type ciCount struct{ failing, running, passing int }

func (c ciCount) rollup() string {
	switch {
	case c.failing > 0:
		return "failing"
	case c.running > 0:
		return "running"
	case c.passing > 0:
		return "passing"
	}
	return "none"
}

// withHeader prepends the ---/+++ file header that hosts omit from per-file hunks.
func withHeader(hunks, oldPath, newPath, status string) string {
	if strings.TrimSpace(hunks) == "" {
		return ""
	}
	from, to := "a/"+oldPath, "b/"+newPath
	switch status {
	case "added":
		from = "/dev/null"
	case "deleted":
		to = "/dev/null"
	}
	if !strings.HasSuffix(hunks, "\n") {
		hunks += "\n"
	}
	return "--- " + from + "\n+++ " + to + "\n" + hunks
}

// countPatch counts added and removed lines inside the hunks of a unified patch.
func countPatch(patch string) (add, del int) {
	_, body, ok := strings.Cut(patch, "\n@@")
	if !ok {
		if !strings.HasPrefix(patch, "@@") {
			return 0, 0
		}
		body = patch
	}
	for _, l := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(l, "+"):
			add++
		case strings.HasPrefix(l, "-"):
			del++
		}
	}
	return add, del
}

// capFiles enforces the file and byte budgets and marks files whose patch the
// host withheld (changed lines but no text: very large diffs).
func capFiles(files []model.PRFile) []model.PRFile {
	total := 0
	for i := range files {
		f := &files[i]
		if f.Patch == "" && f.Additions+f.Deletions > 0 {
			f.Truncated = true
		}
		if i >= maxReviewFiles || total+len(f.Patch) > maxPatchBytes {
			f.Patch = ""
			f.Truncated = true
		}
		total += len(f.Patch)
	}
	return files
}

func sortByUpdated(s []model.PRSummary) {
	sort.SliceStable(s, func(i, j int) bool { return s[i].UpdatedAt.After(s[j].UpdatedAt) })
}
