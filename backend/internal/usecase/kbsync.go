package usecase

import (
	"context"
	"fmt"
	"strings"

	"legos-factory/backend/internal/model"
)

func woQuery(wo *model.WorkOrder) string {
	return wo.Title + " " + wo.Objective + " " + wo.Acceptance
}

func (f *Foreman) taskText(ctx context.Context, woID, taskID string) string {
	if taskID == "" {
		return ""
	}
	tasks, err := f.WO.Plan(ctx, woID)
	if err != nil {
		return ""
	}
	for _, t := range tasks {
		if t.ID == taskID {
			return t.Title + " " + t.Objective
		}
	}
	return ""
}

// syncRepo indexes the work order workspace for its project. A failure is
// logged and reported as an event; it never fails the job.
func (f *Foreman) syncRepo(ctx context.Context, wo *model.WorkOrder, force bool) {
	if f.KB == nil {
		return
	}
	st, sha, skipped, err := f.KB.SyncRepo(ctx, wo.ProjectID, f.workspaceDir(wo.ID), force)
	switch {
	case skipped:
	case err != nil:
		f.Log.Warn("knowledge index failed", "wo", wo.ID, "err", err)
		f.event(wo.ID, "cataloger", "knowledge_index_failed", "Repository indexing failed: "+err.Error(),
			map[string]any{"source": model.KBSourceRepo, "commit_sha": sha, "error": err.Error()})
	default:
		short := sha
		if len(short) > 8 {
			short = short[:8]
		}
		f.event(wo.ID, "cataloger", "knowledge_indexed",
			strings.TrimSpace(fmt.Sprintf("Indexed %d repository document(s) at %s", st.Docs, orUnversioned(short))),
			map[string]any{"source": model.KBSourceRepo, "commit_sha": sha, "docs": st.Docs, "changed": st.Changed, "removed": st.Removed})
	}
}
