package model

// WODiffFile is one file of a work order's committed change; Patch is the
// unified diff from the first @@ on, empty for binary, oversized and
// lock/generated files.
type WODiffFile struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary"`
	Patch     string `json:"patch"`
	Truncated bool   `json:"truncated"`
}

// WODiff is the change a work order made from the merge-base with the
// default branch to its commit. Available is false, with a Reason, when no
// diff can be produced.
type WODiff struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
	Base      string `json:"base"`
	Head      string `json:"head"`
	Revision  int    `json:"revision"`
	// HeadFallback is true when the work order's commit_sha was not found and HEAD was used.
	HeadFallback bool         `json:"head_fallback"`
	Files        []WODiffFile `json:"files"`
	Truncated    bool         `json:"truncated"`
}
