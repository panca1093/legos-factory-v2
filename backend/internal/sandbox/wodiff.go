package sandbox

import (
	"context"
	"os"
	"path"
	"strconv"
	"strings"

	"legos-factory/backend/internal/model"
)

const (
	maxFilePatchBytes  = 200 << 10
	maxTotalPatchBytes = 2 << 20
)

func noDiff(reason string) model.WODiff {
	return model.WODiff{Reason: reason, Files: []model.WODiffFile{}}
}

func skipPatch(p string) bool {
	b := path.Base(p)
	return b == "package-lock.json" || b == "go.sum" || b == "yarn.lock" || b == "pnpm-lock.yaml" || b == "Cargo.lock" ||
		strings.HasSuffix(b, ".min.js") || strings.HasSuffix(b, ".min.css") ||
		strings.HasPrefix(p, "dist/") || strings.Contains(p, "/dist/")
}

func defaultBranchRef(ctx context.Context, dir string) string {
	for _, ref := range []string{"refs/remotes/origin/HEAD", "refs/remotes/origin/main", "refs/remotes/origin/master"} {
		if out, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", ref); err == nil && strings.TrimSpace(out) != "" {
			return ref
		}
	}
	return ""
}

// CommitDiff reports the change from merge-base(origin default branch, head)
// to head, where head falls back to HEAD when sha is empty or unknown.
// Patches are the hunks only, without the "diff --git" preamble.
func (Local) CommitDiff(ctx context.Context, dir, sha string) (model.WODiff, error) {
	if _, err := os.Stat(dir); err != nil {
		return noDiff("The workspace no longer exists."), nil
	}
	if !isLocalGitRepo(dir) {
		return noDiff("The workspace is not a git repository."), nil
	}
	head := ""
	if sha != "" {
		if out, err := gitOut(ctx, dir, "rev-parse", "--verify", "--quiet", sha+"^{commit}"); err == nil {
			head = strings.TrimSpace(out)
		}
	}
	if head == "" {
		out, err := gitOut(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD")
		if err != nil || strings.TrimSpace(out) == "" {
			return noDiff("The workspace has no commits yet."), nil
		}
		head = strings.TrimSpace(out)
	}
	ref := defaultBranchRef(ctx, dir)
	if ref == "" {
		return noDiff("The workspace has no origin default branch to compare against."), nil
	}
	out, err := gitOut(ctx, dir, "merge-base", ref, head)
	if err != nil || strings.TrimSpace(out) == "" {
		return noDiff("No common ancestor with the default branch."), nil
	}
	base := strings.TrimSpace(out)
	if base == head {
		return noDiff("No changes committed yet."), nil
	}

	statusOut, err := gitOut(ctx, dir, "diff", "--name-status", "-z", "-M", base, head)
	if err != nil {
		return noDiff("Could not read the diff: " + err.Error()), nil
	}
	numOut, err := gitOut(ctx, dir, "diff", "--numstat", "-z", "-M", base, head)
	if err != nil {
		return noDiff("Could not read the diff: " + err.Error()), nil
	}
	type counts struct {
		add, del int
		binary   bool
	}
	nums := map[string]counts{}
	nf := strings.Split(numOut, "\x00")
	for i := 0; i < len(nf); i++ {
		parts := strings.SplitN(nf[i], "\t", 3)
		if len(parts) != 3 {
			continue
		}
		p := parts[2]
		if p == "" && i+2 < len(nf) { // rename: "a\td\t\0old\0new\0"
			p = nf[i+2]
			i += 2
		}
		a, _ := strconv.Atoi(parts[0])
		d, _ := strconv.Atoi(parts[1])
		nums[p] = counts{a, d, parts[0] == "-"}
	}

	res := model.WODiff{Available: true, Base: base, Head: head, Files: []model.WODiffFile{}}
	total := 0
	sf := strings.Split(statusOut, "\x00")
	for i := 0; i < len(sf) && sf[i] != ""; i++ {
		code := sf[i][:1]
		f := model.WODiffFile{}
		if code == "R" || code == "C" {
			if i+2 >= len(sf) {
				break
			}
			f.OldPath, f.Path = sf[i+1], sf[i+2]
			f.Status = "renamed"
			i += 2
		} else {
			if i+1 >= len(sf) {
				break
			}
			f.Path = sf[i+1]
			i++
			switch code {
			case "A":
				f.Status = "added"
			case "D":
				f.Status = "deleted"
			default:
				f.Status = "modified"
			}
		}
		n := nums[f.Path]
		f.Additions, f.Deletions, f.Binary = n.add, n.del, n.binary
		switch {
		case f.Binary:
		case skipPatch(f.Path):
			f.Truncated = true
		case total >= maxTotalPatchBytes:
			f.Truncated, res.Truncated = true, true
		default:
			args := []string{"diff", "-M", "--no-color", "--no-ext-diff", "--no-textconv", "-U3", base, head, "--"}
			if f.OldPath != "" {
				args = append(args, f.OldPath)
			}
			patch, err := gitOut(ctx, dir, append(args, f.Path)...)
			if err != nil {
				return noDiff("Could not read the diff: " + err.Error()), nil
			}
			if at := strings.Index(patch, "\n@@"); at >= 0 {
				patch = patch[at+1:]
			} else if !strings.HasPrefix(patch, "@@") {
				patch = ""
			}
			if len(patch) > maxFilePatchBytes {
				patch = patch[:maxFilePatchBytes]
				if nl := strings.LastIndex(patch, "\n"); nl >= 0 {
					patch = patch[:nl+1]
				}
				f.Truncated = true
			}
			if total+len(patch) > maxTotalPatchBytes {
				patch, f.Truncated, res.Truncated = "", true, true
			}
			total += len(patch)
			f.Patch = patch
		}
		res.Files = append(res.Files, f)
	}
	return res, nil
}
