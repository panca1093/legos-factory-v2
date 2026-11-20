package usecase

import (
	"context"

	"legos-factory/backend/internal/kb"
	"legos-factory/backend/internal/model"
)

func woQuery(wo *model.WorkOrder) string {
	return wo.Title + " " + wo.Objective + " " + wo.Acceptance
}

// recordKBBase pins the workspace HEAD as the commit repo playbooks and code facts are read at.
func (f *Factory) recordKBBase(ctx context.Context, wo *model.WorkOrder) {
	if f.KB == nil {
		return
	}
	if sha := kb.GitHead(f.workspaceDir(wo)); sha != "" && sha != wo.KBBaseSHA {
		if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"kb_base_sha": sha}); err != nil {
			f.Log.Warn("knowledge base commit not saved", "wo", wo.ID, "err", err)
		} else {
			wo.KBBaseSHA = sha
		}
	}
	f.seedCodeFacts(ctx, wo)
}
