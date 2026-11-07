package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"legos-factory/backend/internal/model"
)

// Contract tests (contract-flow SPEC "Contract tests"): task t0 writes one test per
// "test" row of the spec's AC -> verification table, the factory proves they fail on an
// assertion (red proof), locks their files by hash, and converge counts a criterion as
// met by code while its test is green and unchanged. The red proof and the lock live in
// "red_proof" events, so a restart reads them back instead of running the proof twice.

const (
	contractTaskRef = "t0"
	// maxRedProofRetries is how many times t0 is sent back, for a failed red proof and a Jev "no" together.
	maxRedProofRetries = 1
)

const contractTestsPlanRule = `

Contract tests are ON for this project. Extra rules:
- The first task has id "t0". It writes one automated test per acceptance criterion whose row in the Acceptance table has Kind "test", and nothing else: no production code, tests that fail until the feature exists. It has no deps.
- Every other task lists "t0" in its deps.
- The first column of the Acceptance table is the criterion id (c1, c2, ...). In the Verification column of each "test" row put the exact test name in backticks, for example ` + "`TestCreateOrderRejectsEmptyCart`" + `.`

func contractTaskID(woID string) string { return woID + "_" + contractTaskRef }

func (f *Factory) contractTestsOn(ctx context.Context, wo *model.WorkOrder) (bool, error) {
	proj, err := f.Proj.Get(ctx, wo.ProjectID)
	if err != nil {
		return false, err
	}
	return proj.ContractTests == "on", nil
}

// requireContractTask makes t0 the root of the plan: it has no deps and every other task depends on it.
func requireContractTask(woID string, tasks []model.Task) error {
	root := contractTaskID(woID)
	if !slices.ContainsFunc(tasks, func(t model.Task) bool { return t.ID == root }) {
		return fmt.Errorf("contract tests are on but the plan has no task %q", contractTaskRef)
	}
	for i := range tasks {
		switch {
		case tasks[i].ID == root:
			tasks[i].Deps = []string{}
		case !slices.Contains(tasks[i].Deps, root):
			tasks[i].Deps = append(tasks[i].Deps, root)
		}
	}
	return nil
}

type contractRow struct {
	Criterion string
	Names     []string
}

var (
	backtick    = regexp.MustCompile("`([^`]+)`")
	goTestName  = regexp.MustCompile(`Test\w+`)
	acRef       = regexp.MustCompile(`\bac\s*(\d+)\b`)
	tableMarker = regexp.MustCompile(`^[\s:|-]+$`)
)

// contractRows reads the "test" rows of the spec's AC -> verification table: the criterion
// in the first column, the test names in the second, the kind in the third.
func contractRows(spec string) []contractRow {
	var rows []contractRow
	for _, line := range strings.Split(spec, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") || tableMarker.MatchString(line) {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 3 || strings.ToLower(strings.Trim(strings.TrimSpace(cells[2]), "`* ")) != "test" {
			continue
		}
		ac := strings.ToLower(cells[0])
		crit := criterionRef.FindString(ac)
		if m := acRef.FindStringSubmatch(ac); crit == "" && m != nil {
			crit = "c" + m[1]
		}
		var names []string
		tokens := backtick.FindAllStringSubmatch(cells[1], -1)
		if len(tokens) == 0 {
			tokens = [][]string{{"", strings.TrimSpace(cells[1])}}
		}
		for _, t := range tokens {
			if found := goTestName.FindAllString(t[1], -1); len(found) > 0 {
				names = append(names, found...)
			} else if n := strings.TrimSpace(t[1]); n != "" {
				names = append(names, n)
			}
		}
		if crit != "" && len(names) > 0 {
			rows = append(rows, contractRow{Criterion: crit, Names: names})
		}
	}
	return rows
}

func contractTestNames(rows []contractRow) []string {
	var names []string
	for _, r := range rows {
		for _, n := range r.Names {
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	return names
}

// buildFailureMarkers are output fragments of a compile, import or setup error. Such a run
// proves nothing about the tests, so it is never red.
var buildFailureMarkers = []string{
	"[build failed]", "[setup failed]", "cannot find package", "no required module", "SyntaxError",
	"Failed to resolve import", "Cannot find module", "Failed to parse source", "Transform failed", "error TS", "Failed to load url",
}

var failLinePrefixes = []string{"--- FAIL", "FAIL", "×", "✕", "✗", "✘", "●"}

// redProofFailure explains why output does not prove every named test fails on an assertion; empty means it does.
func redProofFailure(output string, names []string) string {
	for _, m := range buildFailureMarkers {
		if strings.Contains(output, m) {
			return fmt.Sprintf("the checks hit a build or import error (%q), not a failing assertion; the tests must compile and fail on an assertion", m)
		}
	}
	var passing []string
	for _, n := range names {
		failed := false
		for _, line := range strings.Split(output, "\n") {
			t := strings.TrimSpace(line)
			if strings.Contains(t, n) && slices.ContainsFunc(failLinePrefixes, func(p string) bool { return strings.HasPrefix(t, p) }) {
				failed = true
				break
			}
		}
		if !failed {
			passing = append(passing, n)
		}
	}
	if len(passing) > 0 {
		return "these tests did not fail: " + strings.Join(passing, ", ") + "; each contract test must fail until the feature exists"
	}
	return ""
}

type contractLock struct {
	Path     string   `json:"path"`
	SHA      string   `json:"sha256"`
	Criteria []string `json:"criteria"`
}

func fileSHA(dir, rel string) (string, string) {
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		return "", ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), string(b)
}

// lockContractFiles hashes the files t0 added that hold a contract test.
func lockContractFiles(dir string, changed []string, rows []contractRow) []contractLock {
	var locks []contractLock
	for _, p := range changed {
		if isGenerated(p) {
			continue
		}
		sha, text := fileSHA(dir, p)
		var crits []string
		for _, r := range rows {
			if slices.ContainsFunc(r.Names, func(n string) bool { return strings.Contains(text, n) }) && !slices.Contains(crits, r.Criterion) {
				crits = append(crits, r.Criterion)
			}
		}
		if sha != "" && len(crits) > 0 {
			locks = append(locks, contractLock{Path: p, SHA: sha, Criteria: crits})
		}
	}
	return locks
}

// redProofState reads the red_proof events: the locks of the passing proof, if any, and how many failed.
func (f *Factory) redProofState(ctx context.Context, woID string) (locks []contractLock, passed bool, failures int, err error) {
	evs, err := f.WO.EventsByKind(ctx, woID, []string{"red_proof"})
	if err != nil {
		return nil, false, 0, err
	}
	for _, e := range evs {
		if ok, _ := e.Payload["pass"].(bool); !ok {
			failures++
			continue
		}
		passed, locks = true, nil
		raw, _ := json.Marshal(e.Payload["locks"])
		_ = json.Unmarshal(raw, &locks)
	}
	return locks, passed, failures, nil
}

// contractRedProof runs after t0 is merged. It reports whether the order may go on to the
// other tasks; false means t0 was sent back (ErrRequeue) or the order is parked for a human.
func (f *Factory) contractRedProof(ctx context.Context, wo *model.WorkOrder, job model.Job) (bool, error) {
	if on, err := f.contractTestsOn(ctx, wo); err != nil || !on {
		return true, err
	}
	_, passed, failures, err := f.redProofState(ctx, wo.ID)
	if err != nil || passed {
		return true, err // a restarted job does not prove twice
	}
	rows := contractRows(wo.Spec)
	if len(rows) == 0 {
		f.event(wo.ID, "factory", "red_proof", "No test rows in the acceptance table; nothing to prove", map[string]any{"pass": true, "locks": []contractLock{}})
		return true, nil
	}
	woDir := f.workspaceDir(wo.ID)
	names := contractTestNames(rows)
	why, output, canRetry := f.runRedProof(ctx, wo, woDir, names)
	var reasons, jevNo []string
	if why != "" {
		reasons = append(reasons, why)
	} else {
		jevNo = f.askTestsCheck(ctx, wo, woDir, rows)
		for _, c := range jevNo {
			reasons = append(reasons, fmt.Sprintf("Jev judged that the tests for %s do not really check it", c))
		}
	}
	payload := map[string]any{"attempt": failures + 1, "tests": names, "reasons": reasons, "output": output, "jev_no": jevNo}
	if len(reasons) == 0 {
		changed, err := f.Sandbox.ChangedFiles(ctx, woDir)
		if err != nil {
			return false, err
		}
		locks := lockContractFiles(woDir, changed, rows)
		payload["pass"], payload["locks"] = true, locks
		if err := f.setBuilderNotes(ctx, wo.ID, "red proof", nil); err != nil {
			return false, err
		}
		f.event(wo.ID, "factory", "red_proof", fmt.Sprintf("Red proof passed: %d contract test(s) fail on an assertion; %d file(s) locked", len(names), len(locks)), payload)
		return true, nil
	}
	payload["pass"] = false
	if canRetry && failures < maxRedProofRetries {
		note := "REQUIRED FIX for the contract tests (task t0): " + strings.Join(reasons, "; ") + "."
		if output != "" {
			note += "\nCheck output (tail):\n" + output
		}
		if err := f.setBuilderNotes(ctx, wo.ID, "red proof", []string{note}); err != nil {
			return false, err
		}
		payload["retry"] = true
		f.event(wo.ID, "factory", "red_proof", "Red proof failed; t0 goes back to the Builder once: "+strings.Join(reasons, "; "), payload)
		return false, model.ErrRequeue
	}
	question := "The contract tests failed their red proof: " + strings.Join(reasons, "; ") + ". Should the work go on without them being locked?"
	if err := f.parkForHuman(ctx, wo, job, "factory", question); err != nil {
		return false, err
	}
	payload["escalated"] = true
	f.event(wo.ID, "factory", "red_proof", "Red proof failed again; waiting for a human", payload)
	return false, nil
}

// runRedProof runs the project's checks in dir and returns why the output does not prove every
// test red (empty when it does) with the tail of that output. canRetry is false when sending t0
// back cannot help (the project has no checks).
func (f *Factory) runRedProof(ctx context.Context, wo *model.WorkOrder, dir string, names []string) (why, tail string, canRetry bool) {
	profile, err := f.profile(ctx, wo)
	if err != nil || len(profile.Checks) == 0 {
		return "the project has no checks to run the contract tests", "", false
	}
	var out strings.Builder
	for _, c := range profile.Checks {
		r := f.Sandbox.RunCheck(ctx, dir, c, f.QualTimeout)
		fmt.Fprintf(&out, "$ %s\n%s\n", c.Command, r.Output)
	}
	why = redProofFailure(out.String(), names)
	if why == "" {
		return "", "", true
	}
	tail = out.String()
	if len(tail) > 3000 {
		tail = tail[len(tail)-3000:]
	}
	return why, tail, true
}

// askTestsCheck asks Jev once per criterion whether its test really checks it and returns the criteria answered no.
// Without Jev, or when it fails, nothing blocks: converge still asks met_<c> for a criterion it cannot trust.
func (f *Factory) askTestsCheck(ctx context.Context, wo *model.WorkOrder, dir string, rows []contractRow) []string {
	if f.Judge == nil {
		return nil
	}
	diff, derr := f.Sandbox.Diff(ctx, dir)
	if derr != nil {
		f.event(wo.ID, "factory", "diff_unavailable", derr.Error(), nil)
	}
	criteria := splitCriteria(wo.Acceptance)
	trimmed, _ := trimHunks(diff, diffHunkBudget)
	state := map[string]any{"acceptance_criteria": orderedCriteria(criteria), "spec": wo.Spec, "tests": trimmed}
	qs := map[string]Noul{}
	var ids []string
	for _, r := range rows {
		if !slices.ContainsFunc(criteria, func(c Criterion) bool { return c.ID == r.Criterion }) {
			continue
		}
		id := "test_checks_" + r.Criterion
		qs[id] = Noul{
			Instructions: fmt.Sprintf("Do the tests %s in tests really check the behaviour acceptance_criteria.%s describes?", strings.Join(r.Names, ", "), r.Criterion),
			Criteria: yn(
				fmt.Sprintf("The named tests assert the behaviour acceptance_criteria.%s describes, so an implementation that gets it wrong would fail them.", r.Criterion),
				fmt.Sprintf("The named tests are missing from tests, assert something else, or would pass for a wrong implementation of acceptance_criteria.%s.", r.Criterion)),
		}
		ids = append(ids, r.Criterion)
	}
	if len(qs) == 0 {
		return nil
	}
	res, over, jerr := f.askJudge(ctx, state, qs)
	var no []string
	for _, c := range ids {
		if jerr == nil && !over && judge(res.Answers["test_checks_"+c]) == vNo {
			no = append(no, c)
		}
	}
	out := GateOutcome{Outcome: "tests_ok"}
	if len(no) > 0 {
		out.Outcome, out.Row, out.Note = "tests_back", 1, "tests for "+strings.Join(no, ", ")+" do not check their criterion"
	}
	f.recordGate(wo.ID, "tests", state, qs, res, out, jerr)
	return no
}

// contractStatus reports, for converge, the criteria met by a green unchanged contract test and a
// contract-layer gap for every locked test file that changed after its red proof.
func (f *Factory) contractStatus(ctx context.Context, wo *model.WorkOrder) (tested []string, gaps []convergeGap, err error) {
	on, err := f.contractTestsOn(ctx, wo)
	if err != nil || !on {
		return nil, nil, err
	}
	locks, passed, _, err := f.redProofState(ctx, wo.ID)
	if err != nil || !passed {
		return nil, nil, err
	}
	woDir := f.workspaceDir(wo.ID)
	changed := map[string]bool{}
	for _, l := range locks {
		if sha, _ := fileSHA(woDir, l.Path); sha != l.SHA {
			for _, c := range l.Criteria {
				changed[c] = true
			}
			gaps = append(gaps, convergeGap{
				ID: fmt.Sprintf("H%d", len(gaps)+1), Type: "contradicts", Layer: "contract", Cites: strings.Join(l.Criteria, ", "), Evidence: l.Path, Restore: true,
				Fix: fmt.Sprintf("contract test changed: %s no longer matches the version locked at red proof; restore it, or amend the spec with the reason the criterion changed", l.Path),
			})
		}
	}
	for _, l := range locks {
		for _, c := range l.Criteria {
			if !changed[c] && !slices.Contains(tested, c) {
				tested = append(tested, c)
			}
		}
	}
	return tested, gaps, nil
}
