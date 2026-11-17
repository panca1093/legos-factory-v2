package usecase

import (
	"context"
	"path/filepath"

	"legos-factory/backend/internal/model"
)

// runProfileChecks runs every check of p in dir for one named run (a database-safe name such as run_<wo>_<rev>).
// Optional checks are run and returned like the others; callers decide what counts.
func (f *Factory) runProfileChecks(ctx context.Context, projectID, runName, dir string, p model.Profile) ([]model.CheckResult, error) {
	out := make([]model.CheckResult, 0, len(p.Checks))
	for _, c := range p.Checks {
		out = append(out, f.Sandbox.RunCheck(ctx, filepath.Join(dir, c.Dir), c, f.QualTimeout))
	}
	return out, nil
}
