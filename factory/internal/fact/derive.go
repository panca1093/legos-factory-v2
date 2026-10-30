package fact

import "time"

type Status string

const (
	StatusQueued    Status = "queued"

	// StatusAwaitingHuman is never returned by Derive; the engine reads it from the workflow state.
	StatusAwaitingHuman Status = "awaiting_human"
	StatusBuilding  Status = "building"
	StatusVerifying Status = "verifying"
	StatusPROpen    Status = "pr_open"
	StatusCIFailed  Status = "ci_failed"
	StatusCIGreen   Status = "ci_green"
	StatusMerged    Status = "merged"
	StatusClosed    Status = "closed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

func (s Status) Terminal() bool {
	switch s {
	case StatusMerged, StatusClosed, StatusFailed, StatusCancelled:
		return true
	}
	return false
}

const StallAfter = 3 * time.Minute

// Derive folds facts in append order; later facts override earlier ones and a terminal status is final.
func Derive(facts []Fact) Status {
	status := StatusQueued
	var head string
	checks := map[string]string{}
	stable := false

	for _, f := range facts {
		switch f.Kind {
		case KindWOCancelled:
			return StatusCancelled
		case KindWOFailed:
			return StatusFailed
		case KindPRMerged:
			return StatusMerged
		case KindPRClosed:
			return StatusClosed
		case KindRunStarted, KindSandboxStarted, KindTaskCommitted, KindVerifyFailed, KindWORequeued:
			status = StatusBuilding
		case KindVerifyPassed:
			status = StatusVerifying
		case KindPROpened:
			p, err := Decode[PROpened](f)
			if err != nil {
				continue
			}
			head, checks, stable, status = p.HeadSHA, map[string]string{}, false, StatusPROpen
		case KindPRPushed:
			p, err := Decode[PRPushed](f)
			if err != nil {
				continue
			}
			head, checks, stable, status = p.HeadSHA, map[string]string{}, false, StatusPROpen
		case KindCheckObserved:
			if status != StatusPROpen && status != StatusCIFailed && status != StatusCIGreen {
				continue
			}
			c, err := Decode[CheckObserved](f)
			if err != nil || c.HeadSHA != head {
				continue
			}
			checks[c.Name], stable = c.State, false
			status = checkStatus(checks, stable)
		case KindChecksStable:
			if status != StatusPROpen && status != StatusCIFailed && status != StatusCIGreen {
				continue
			}
			c, err := Decode[ChecksStable](f)
			if err != nil || c.HeadSHA != head {
				continue
			}
			stable = true
			status = checkStatus(checks, stable)
		}
	}
	return status
}

// checkStatus: green needs a checks.stable fact on top of all-success, because a late-registering workflow looks the same as a finished set.
func checkStatus(checks map[string]string, stable bool) Status {
	for _, st := range checks {
		if st == "failure" {
			return StatusCIFailed
		}
	}
	if stable {
		return StatusCIGreen
	}
	return StatusPROpen
}

// Stalled reports a run that started, has not finished, and shows no activity for StallAfter.
func Stalled(facts []Fact, now time.Time) bool {
	start := -1
	for i, f := range facts {
		if f.Kind == KindRunStarted {
			start = i
		}
	}
	if start < 0 {
		return false
	}
	started, err := Decode[RunStarted](facts[start])
	if err != nil {
		return false
	}
	last := facts[start].At
	for _, f := range facts[start+1:] {
		switch f.Kind {
		case KindRunFinished:
			if r, err := Decode[RunFinished](f); err == nil && r.Run == started.Run {
				return false
			}
		case KindRunStep:
			if r, err := Decode[RunStep](f); err == nil && r.Run == started.Run && f.At.After(last) {
				last = f.At
			}
		}
	}
	return now.Sub(last) > StallAfter
}
