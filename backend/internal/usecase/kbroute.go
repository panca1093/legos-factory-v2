package usecase

import (
	"context"
	"encoding/json"
	"time"

	"legos-factory/backend/internal/kb"
	"legos-factory/backend/internal/model"
)

func routesOf(meta map[string]any) []model.ReviewerRoute {
	raw, ok := meta["routes"]
	if !ok {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var routes []model.ReviewerRoute
	if json.Unmarshal(data, &routes) != nil {
		return nil
	}
	for _, r := range routes {
		if len(r.Paths) == 0 || len(r.Reviewers) == 0 {
			return nil
		}
	}
	return routes
}

func routingUsed(d model.KBDoc) map[string]any {
	return map[string]any{"id": d.ID, "status": d.Status, "source": d.Source}
}

// reviewers resolves the specialists for the changed files. Order: the repo's
// Reviewer Routing doc, the project's factory doc, then a draft proposed from
// the path heuristics (still used until a human replaces it). The second
// result describes the routing doc used, for the audit event.
func (f *Foreman) reviewers(ctx context.Context, wo *model.WorkOrder, files []string) ([]SpecialistPick, map[string]any) {
	k := f.KB
	if k == nil {
		return SelectSpecialists(files), nil
	}
	pid := wo.ProjectID
	repoDocs, err := k.Idx.List(ctx, model.KBQuery{
		ProjectID: pid, Sources: []string{model.KBSourceRepo}, Types: []string{model.KBTypeReviewerRoute}, Limit: 50,
	})
	if err != nil {
		f.Log.Warn("routing: repo lookup failed", "wo", wo.ID, "err", err)
	}
	for _, d := range repoDocs {
		if r := routesOf(d.Meta); len(r) > 0 {
			return MatchReviewers(r, files), routingUsed(d)
		}
	}
	rel := "projects/" + pid + "/reviewer-routing.md"
	d, err := k.Idx.Get(ctx, kb.DocID(model.KBSourceFactory, pid, rel))
	if err != nil {
		f.Log.Warn("routing: factory lookup failed", "wo", wo.ID, "err", err)
		return SelectSpecialists(files), nil
	}
	if d != nil {
		if r := routesOf(d.Meta); len(r) > 0 && d.Status != model.KBStatusDeprecated {
			return MatchReviewers(r, files), routingUsed(*d)
		}
		return MatchReviewers(ProposeRoutes(files), files), map[string]any{"id": d.ID, "status": d.Status, "source": "heuristic"}
	}
	routes := ProposeRoutes(files)
	if len(routes) == 0 {
		return SelectSpecialists(files), nil
	}
	front := map[string]any{
		"type": model.KBTypeReviewerRoute, "title": "Reviewer routing", "status": model.KBStatusDraft,
		"description": "Proposed from path heuristics at the first inspection; edit and approve it to confirm.",
		"routes":      routes,
		"generated":   map[string]any{"by": "legos-factory/routing", "at": time.Now().UTC().Format(time.RFC3339)},
	}
	body := "# Reviewer routing\n\nProposed by the factory from the files changed in " + wo.ID + ". Each route sends files matching its paths to the listed reviewers; `!` excludes a path. Testing always reviews.\n"
	if err := k.WriteFactory(ctx, rel, front, body, "Propose reviewer routing for "+wo.ProjectID); err != nil {
		f.Log.Warn("routing: could not write proposal", "wo", wo.ID, "err", err)
		return MatchReviewers(routes, files), map[string]any{"status": model.KBStatusDraft, "source": "heuristic"}
	}
	id := kb.DocID(model.KBSourceFactory, pid, rel)
	f.event(wo.ID, "foreman", "routing_proposed",
		"No reviewer routing exists for this project; a draft was proposed from path heuristics. Review and approve it in Knowledge.",
		map[string]any{"doc_id": id, "path": rel})
	return MatchReviewers(routes, files), map[string]any{"id": id, "status": model.KBStatusDraft, "source": model.KBSourceFactory}
}
