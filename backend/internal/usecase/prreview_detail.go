package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"legos-factory/backend/internal/model"
)

const (
	maxReviewFiles    = 300
	maxReviewPatchSum = 1 << 20
)

func patchHash(patch string) string {
	sum := sha256.Sum256([]byte(patch))
	return hex.EncodeToString(sum[:])
}

func (r *Reviews) detail(ctx context.Context, rc *reviewCtx, number int, fresh bool) (model.PRDetail, error) {
	key := fmt.Sprintf("detail|%s|%s|%d", rc.key, rc.repo, number)
	v, err := r.memo(key, r.ttl(), fresh, func() (any, error) {
		return retryGet(ctx, func() (model.PRDetail, error) { return rc.rv.GetPullRequest(ctx, rc.repo, number) })
	})
	if err != nil {
		return model.PRDetail{}, err
	}
	return v.(model.PRDetail), nil
}

// files are the PR's per-file patches at one head; past 300 files or 1 MB of patch text the rest are
// listed without a patch, so nothing anchors there.
func (r *Reviews) files(ctx context.Context, rc *reviewCtx, number int, head string) ([]model.PRFile, error) {
	key := fmt.Sprintf("files|%s|%s|%d|%s", rc.key, rc.repo, number, head)
	v, err := r.memo(key, 10*meTTL, false, func() (any, error) {
		fs, err := retryGet(ctx, func() ([]model.PRFile, error) { return rc.rv.PullRequestFiles(ctx, rc.repo, number) })
		if err != nil {
			return nil, err
		}
		sum := 0
		for i := range fs {
			if i >= maxReviewFiles || sum+len(fs[i].Patch) > maxReviewPatchSum {
				fs[i].Patch, fs[i].Truncated = "", true
				continue
			}
			sum += len(fs[i].Patch)
		}
		return fs, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]model.PRFile), nil
}

func (r *Reviews) verdicts(rc *reviewCtx, d model.PRDetail) model.Verdicts {
	v := model.Verdicts{Approve: model.Availability{OK: true}, RequestChanges: model.Availability{OK: true}}
	if rc.provider == model.ProviderGitHub && sameUser(d.Author, rc.me) {
		const why = "GitHub does not let you approve or request changes on your own pull request"
		v.Approve, v.RequestChanges = model.Availability{Reason: why}, model.Availability{Reason: why}
	}
	if rc.provider == model.ProviderGitLab {
		v.RequestChanges = model.Availability{Reason: "Requesting changes is not supported on GitLab through its API"}
	}
	return v
}

func aiAvailability(rc *reviewCtx, d model.PRDetail) model.Availability {
	if d.SourceRepo != "" && !strings.EqualFold(d.SourceRepo, rc.repo) {
		return model.Availability{Reason: "AI review is not available for fork pull requests yet"}
	}
	return model.Availability{OK: true}
}

// Detail is the PR with the user's local review state, run and what each verdict allows.
func (r *Reviews) Detail(ctx context.Context, u *model.User, projectID string, number int) (*model.PRDetailView, error) {
	rc, err := r.open(ctx, u, projectID, false)
	if err != nil {
		return nil, err
	}
	d, err := r.detail(ctx, rc, number, false)
	if err != nil {
		return nil, err
	}
	out := &model.PRDetailView{PR: d, Identity: r.identityView(ctx, rc), Verdicts: r.verdicts(rc, d), AI: aiAvailability(rc, d)}
	if out.Review, err = r.review(ctx, rc, number); err != nil || out.Review == nil {
		return out, err
	}
	if out.Run, err = r.Store.LatestRun(ctx, out.Review.ID); err != nil && !missing(err) {
		return nil, err
	}
	if _, running := r.running.Load(out.Review.ID); running && out.Run != nil && out.Run.State != model.ReviewRunRunning {
		out.Run.State = model.ReviewRunRunning
	}
	return out, nil
}

// Files lists the PR's files with the user's viewed marks, and outdates the drafts whose file changed
// since the head they were made on.
func (r *Reviews) Files(ctx context.Context, u *model.User, projectID string, number int) ([]model.PRFileView, error) {
	rc, err := r.open(ctx, u, projectID, false)
	if err != nil {
		return nil, err
	}
	d, err := r.detail(ctx, rc, number, false)
	if err != nil {
		return nil, err
	}
	files, err := r.files(ctx, rc, number, d.HeadSHA)
	if err != nil {
		return nil, err
	}
	viewed := map[string]string{}
	rev, err := r.review(ctx, rc, number)
	if err != nil {
		return nil, err
	}
	if rev != nil {
		if viewed, err = r.Store.Viewed(ctx, rev.ID); err != nil {
			return nil, err
		}
		if err := r.outdateDrafts(ctx, rev, d.HeadSHA, files, viewed); err != nil {
			return nil, err
		}
	}
	out := make([]model.PRFileView, len(files))
	for i, f := range files {
		h, ok := viewed[f.Path]
		out[i] = model.PRFileView{PRFile: f, Viewed: ok && h == patchHash(f.Patch)}
	}
	return out, nil
}

// outdateDrafts marks undecided and accepted drafts outdated when the PR head moved and their file's
// patch is not provably the same: only files the user viewed keep a hash to compare with.
func (r *Reviews) outdateDrafts(ctx context.Context, rev *model.PRReview, head string, files []model.PRFile, viewed map[string]string) error {
	if rev.HeadSHA == "" || rev.HeadSHA == head {
		return nil
	}
	ds, err := r.Store.Drafts(ctx, rev.ID)
	if err != nil {
		return err
	}
	cur := map[string]string{}
	for _, f := range files {
		cur[f.Path] = patchHash(f.Patch)
	}
	seen := map[string]bool{}
	var paths []string
	for _, d := range ds {
		if (d.Status != model.DraftPending && d.Status != model.DraftAccepted) || d.HeadSHA == head || seen[d.Path] {
			continue
		}
		seen[d.Path] = true
		if h, ok := cur[d.Path]; !ok || viewed[d.Path] != h {
			paths = append(paths, d.Path)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	return r.Store.MarkOutdated(ctx, rev.ID, paths)
}

// SetViewed marks a file viewed (against its current patch) or not, and reports the counts.
func (r *Reviews) SetViewed(ctx context.Context, u *model.User, projectID string, number int, path string, viewed bool) (*model.ViewedCount, error) {
	rc, err := r.open(ctx, u, projectID, false)
	if err != nil {
		return nil, err
	}
	d, err := r.detail(ctx, rc, number, false)
	if err != nil {
		return nil, err
	}
	files, err := r.files(ctx, rc, number, d.HeadSHA)
	if err != nil {
		return nil, err
	}
	var target *model.PRFile
	for i := range files {
		if files[i].Path == path {
			target = &files[i]
		}
	}
	if target == nil {
		return nil, fmt.Errorf("%w: %s is not a file of this pull request", ErrInvalid, path)
	}
	rev, err := r.ensureReview(ctx, rc, number)
	if err != nil {
		return nil, err
	}
	if err := r.Store.SetViewed(ctx, rev.ID, path, patchHash(target.Patch), viewed); err != nil {
		return nil, err
	}
	marks, err := r.Store.Viewed(ctx, rev.ID)
	if err != nil {
		return nil, err
	}
	n := 0
	for _, f := range files {
		if h, ok := marks[f.Path]; ok && h == patchHash(f.Patch) {
			n++
		}
	}
	return &model.ViewedCount{Viewed: n, Total: len(files)}, nil
}

// locate authorizes u on the project and finds its repository without touching the provider.
func (r *Reviews) locate(ctx context.Context, u *model.User, projectID string) (*reviewCtx, error) {
	p, err := r.Projects.Get(ctx, u, projectID)
	if err != nil {
		return nil, err
	}
	host, repo := parseRemote(r.Integrations.origin(ctx, p))
	if host == "" || repo == "" {
		return nil, fmt.Errorf("%w: this project has no remote origin on a git host", ErrNoReviewIdentity)
	}
	provider := providerForHost(host)
	if provider == "" {
		conns, err := r.Integrations.Store.Visible(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		for _, c := range conns {
			if c.OwnerID == u.ID && c.IsGit() && connectionHost(c.BaseURL) == host {
				provider = c.Provider
			}
		}
	}
	if provider == "" {
		return nil, ErrNoReviewIdentity
	}
	return &reviewCtx{project: p, userID: u.ID, provider: provider, host: host, repo: repo}, nil
}

// Drafts lists the review's drafts; a PR never opened for review has none.
func (r *Reviews) Drafts(ctx context.Context, u *model.User, projectID string, number int) ([]model.PRDraft, error) {
	rc, err := r.locate(ctx, u, projectID)
	if err != nil {
		return nil, err
	}
	rev, err := r.review(ctx, rc, number)
	if err != nil || rev == nil {
		return []model.PRDraft{}, err
	}
	ds, err := r.Store.Drafts(ctx, rev.ID)
	if ds == nil {
		ds = []model.PRDraft{}
	}
	return ds, err
}

var draftMoves = map[string]map[string]bool{
	model.DraftPending:   {model.DraftAccepted: true, model.DraftDiscarded: true},
	model.DraftDiscarded: {model.DraftPending: true},
	model.DraftAccepted:  {model.DraftPending: true},
}

// UpdateDraft applies a decision or an edit. Only pending ↔ accepted, pending ↔ discarded are legal;
// posted, superseded and outdated drafts are fixed, and only open drafts take a new body.
func (r *Reviews) UpdateDraft(ctx context.Context, u *model.User, projectID string, number int, draftID string, status, body *string) (*model.PRDraft, error) {
	rc, err := r.locate(ctx, u, projectID)
	if err != nil {
		return nil, err
	}
	rev, err := r.requireReview(ctx, rc, number)
	if err != nil {
		return nil, err
	}
	ds, err := r.Store.Drafts(ctx, rev.ID)
	if err != nil {
		return nil, err
	}
	var cur *model.PRDraft
	for i := range ds {
		if ds[i].ID == draftID {
			cur = &ds[i]
		}
	}
	if cur == nil {
		return nil, ErrReviewNotFound
	}
	if status != nil && *status != cur.Status && !draftMoves[cur.Status][*status] {
		return nil, fmt.Errorf("%w: a %s draft cannot become %s", ErrInvalid, cur.Status, *status)
	}
	if status != nil && !isDraftStatus(*status) {
		return nil, fmt.Errorf("%w: unknown draft status %q", ErrInvalid, *status)
	}
	if body != nil {
		if cur.Status != model.DraftPending && cur.Status != model.DraftAccepted {
			return nil, fmt.Errorf("%w: a %s draft cannot be edited", ErrInvalid, cur.Status)
		}
		if strings.TrimSpace(*body) == "" {
			return nil, fmt.Errorf("%w: a comment needs text", ErrInvalid)
		}
	}
	if status != nil && *status == cur.Status {
		status = nil
	}
	if status == nil && body == nil {
		return cur, nil
	}
	return r.Store.UpdateDraft(ctx, rev.ID, draftID, status, body)
}

func isDraftStatus(s string) bool {
	switch s {
	case model.DraftPending, model.DraftAccepted, model.DraftDiscarded, model.DraftSuperseded, model.DraftOutdated, model.DraftPosted:
		return true
	}
	return false
}

// UpdateReview sets the summary and verdict the post will use.
func (r *Reviews) UpdateReview(ctx context.Context, u *model.User, projectID string, number int, summary, verdict *string) (*model.PRReview, error) {
	rc, err := r.locate(ctx, u, projectID)
	if err != nil {
		return nil, err
	}
	if verdict != nil {
		switch *verdict {
		case model.VerdictComment, model.VerdictApprove, model.VerdictRequestChanges:
		default:
			return nil, fmt.Errorf("%w: verdict must be comment, approve or request_changes", ErrInvalid)
		}
	}
	rev, err := r.ensureReview(ctx, rc, number)
	if err != nil {
		return nil, err
	}
	s, v := rev.Summary, rev.Verdict
	if summary != nil {
		s = *summary
	}
	if verdict != nil {
		v = *verdict
	}
	if err := r.Store.SetDecision(ctx, rev.ID, s, v); err != nil {
		return nil, err
	}
	return r.requireReview(ctx, rc, number)
}

// Post sends the accepted drafts, summary and verdict as one review under the user's own name. It posts
// only when the client confirmed what the server still holds, and never retries on its own. When the
// review was already posted at this head, a post sends only the accepted drafts still waiting (the ones
// that failed before), without the summary and verdict again.
func (r *Reviews) Post(ctx context.Context, u *model.User, projectID string, number int, req model.PostRequest) (*model.PostedReview, error) {
	if !req.Confirm {
		return nil, fmt.Errorf("%w: confirm must be true to post", ErrInvalid)
	}
	rc, err := r.open(ctx, u, projectID, true)
	if err != nil {
		return nil, err
	}
	rev, err := r.requireReview(ctx, rc, number)
	if err != nil {
		return nil, err
	}
	if _, busy := r.posting.LoadOrStore(rev.ID, struct{}{}); busy {
		return nil, ErrPostInFlight
	}
	defer r.posting.Delete(rev.ID)
	d, err := r.detail(ctx, rc, number, true)
	if err != nil {
		return nil, err
	}
	all, err := r.Store.Drafts(ctx, rev.ID)
	if err != nil {
		return nil, err
	}
	var inline, general []model.PRDraft
	for _, dr := range all {
		if dr.Status != model.DraftAccepted {
			continue
		}
		if dr.Inline {
			inline = append(inline, dr)
		} else {
			general = append(general, dr)
		}
	}
	retry := rev.PostedAt != nil && sameHead(rev.PostedHeadSHA, d.HeadSHA)
	current := model.PostExpect{Login: rc.me.Login, HeadSHA: d.HeadSHA, Comments: len(inline) + len(general), Verdict: rev.Verdict}
	conflict := func(reason string) error { return &PostConflict{Reason: reason, Current: current} }
	switch {
	case !strings.EqualFold(req.Expect.Login, current.Login):
		return nil, conflict("the posting account changed")
	case req.Expect.HeadSHA != current.HeadSHA:
		return nil, conflict("the pull request was updated")
	case req.Expect.Comments != current.Comments || req.Expect.Verdict != current.Verdict:
		return nil, conflict("the review changed since you confirmed it")
	}
	var stale []string
	for _, dr := range slices.Concat(inline, general) {
		if dr.HeadSHA != d.HeadSHA {
			stale = append(stale, fmt.Sprintf("%q on %s", dr.Title, dr.Path))
		}
	}
	if len(stale) > 0 {
		return nil, conflict(fmt.Sprintf("the pull request was updated since %d accepted comment(s) were drafted (%s): re-run the review or discard them", len(stale), strings.Join(stale, ", ")))
	}
	verdict, summary := rev.Verdict, rev.Summary
	if retry {
		verdict, summary = model.VerdictComment, ""
	} else {
		vd := r.verdicts(rc, d)
		if verdict == model.VerdictApprove && !vd.Approve.OK {
			return nil, fmt.Errorf("%w: %s", ErrInvalid, vd.Approve.Reason)
		}
		if verdict == model.VerdictRequestChanges && !vd.RequestChanges.OK {
			return nil, fmt.Errorf("%w: %s", ErrInvalid, vd.RequestChanges.Reason)
		}
	}
	if info, err := r.tokenInfo(ctx, rc); err == nil && info.CanWrite == model.TriNo {
		return nil, ErrReadOnlyToken
	}
	body := postBody(summary, general)
	if body == "" && len(inline) == 0 && verdict != model.VerdictApprove {
		return nil, fmt.Errorf("%w: nothing to post: accept a comment or write a summary", ErrInvalid)
	}
	if body == "" && verdict == model.VerdictRequestChanges {
		return nil, fmt.Errorf("%w: requesting changes needs a summary", ErrInvalid)
	}
	sub := model.ReviewSubmission{HeadSHA: d.HeadSHA, Body: body, Verdict: verdict}
	for _, dr := range inline {
		sub.Comments = append(sub.Comments, model.InlineComment{DraftID: dr.ID, Path: dr.Path, OldPath: dr.OldPath, Side: dr.Side,
			Line: dr.Line, OldLine: dr.OldLine, Body: dr.Body, PostedRef: dr.PostedRef})
	}
	posted, perr := rc.rv.PostReview(ctx, rc.repo, number, sub)
	// a provider that posts comment by comment may fail midway: what it did post is recorded before the error returns
	partial := len(posted.Comments)+len(posted.Failed) > 0
	if perr == nil || partial {
		for _, dr := range inline {
			var err error
			if ref, ok := posted.Comments[dr.ID]; ok {
				err = r.Store.SetPosted(ctx, dr.ID, ref, "")
			} else {
				msg := posted.Failed[dr.ID]
				if msg == "" {
					msg = "the provider did not confirm this comment"
					if perr != nil {
						msg = perr.Error()
					}
				}
				err = r.Store.SetPosted(ctx, dr.ID, "", msg)
			}
			if err != nil {
				return &posted, err
			}
		}
	}
	verdictDone := perr == nil && verdict != model.VerdictComment && posted.VerdictErr == ""
	if posted.Ref != "" || len(posted.Comments) > 0 || verdictDone {
		for _, dr := range general {
			if err := r.Store.SetPosted(ctx, dr.ID, posted.Ref, ""); err != nil {
				return &posted, err
			}
		}
		if !retry {
			recorded := verdict
			if posted.VerdictErr != "" || perr != nil {
				recorded = model.VerdictComment
			}
			if err := r.Store.MarkPosted(ctx, rev.ID, d.HeadSHA, recorded, posted.Ref, rc.me.Login, r.now()); err != nil {
				return &posted, err
			}
		}
	}
	return &posted, perr
}

// sameHead compares commit ids where one side may be a short hash (Bitbucket lists carry 12 characters).
func sameHead(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return len(a) >= 7 && strings.HasPrefix(b, a)
}

// postBody is the review summary followed by the accepted comments that could not be anchored in the diff.
func postBody(summary string, general []model.PRDraft) string {
	parts := []string{}
	if s := strings.TrimSpace(summary); s != "" {
		parts = append(parts, s)
	}
	if len(general) > 0 {
		var b strings.Builder
		b.WriteString("General comments:")
		for _, g := range general {
			where := ""
			if g.Path != "" {
				where = " (`" + g.Path + "`)"
			}
			fmt.Fprintf(&b, "\n- **%s**%s: %s", g.Title, where, strings.TrimSpace(g.Body))
		}
		parts = append(parts, b.String())
	}
	return strings.Join(parts, "\n\n")
}
