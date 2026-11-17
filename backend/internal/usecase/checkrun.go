package usecase

import (
	"context"

	"legos-factory/backend/internal/model"
)

// runProfileChecks runs every check of p in dir for one named run (a database-safe name such as run_<wo>_<rev>).
// Optional checks are run and returned like the others; callers decide what counts.
// Results line up with p.Checks. When the run cannot get its services every check is UNVERIFIED with the reason, never PASS.
func (f *Factory) runProfileChecks(ctx context.Context, projectID, runName, dir string, p model.Profile) ([]model.CheckResult, error) {
	out, _ := f.runChecks(ctx, projectID, runName, dir, p)
	return out, nil
}

// runChecks is runProfileChecks that also returns the lease or setup error that made every check UNVERIFIED, so callers can tell infrastructure from a failing check.
func (f *Factory) runChecks(ctx context.Context, projectID, runName, dir string, p model.Profile) (out []model.CheckResult, infra error) {
	env, release, err := f.leaseRun(ctx, projectID, runName, dir, p)
	defer release()
	out = make([]model.CheckResult, 0, len(p.Checks))
	for _, c := range p.Checks {
		if err != nil {
			out = append(out, model.CheckResult{Name: c.Name, Command: c.Command, Outcome: model.Unverified, Output: "check services unavailable: " + err.Error()})
			continue
		}
		out = append(out, f.Sandbox.RunCheck(ctx, dir, c, f.QualTimeout, env))
	}
	return out, err
}
