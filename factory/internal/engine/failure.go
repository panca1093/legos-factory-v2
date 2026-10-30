package engine

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/sandbox"
	"legos-factory/factory/internal/workflow"
)

const (
	defaultRetryBase      = 2 * time.Second
	defaultTransientTries = 4
	maxInfraRestarts      = 3
	maxTransientRetries   = 12
	retryBackoffCap       = 2 * time.Minute
)

// classedError tags an error with its ADR-F10 failure class and a short reason.
type classedError struct {
	class, reason string
	err           error
}

func (c *classedError) Error() string { return c.err.Error() }
func (c *classedError) Unwrap() error { return c.err }

func classed(class, reason string, err error) error {
	return &classedError{class: class, reason: reason, err: err}
}

// Failure is the verdict of Classify.
type Failure struct {
	Class     string
	Reason    string
	Transient bool // provider only: the engine retries with backoff
}

// Classify maps an error to its failure class (ADR-F10). sandboxDown is the result of a liveness probe of the sandbox
// the error came from. Cancellation is not a failure and yields the zero Failure.
func Classify(err error, sandboxDown bool) Failure {
	var ce *classedError
	switch {
	case err == nil, errors.Is(err, context.Canceled):
		return Failure{}
	case errors.As(err, &ce):
		return Failure{Class: ce.class, Reason: ce.reason}
	case isProviderErr(err):
		return Failure{Class: fact.ClassProvider, Transient: IsTransient(err)}
	case sandboxDown:
		return Failure{Class: fact.ClassInfra, Reason: "sandbox_down"}
	}
	return Failure{Class: fact.ClassTask, Reason: "error"}
}

var probeTimeout = 10 * time.Second // a hung sandbox must not hang the probe

// sandboxDown probes the sandbox with a trivial command.
func sandboxDown(ctx context.Context, sb sandbox.Sandbox) bool {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	_, _, code, err := sb.Exec(ctx, "", nil, "true")
	return err != nil || code != 0
}

// budgetUsed counts sandbox restarts and provider retries recorded since the work order was last retried.
func (e *Engine) budgetUsed(ctx context.Context, woID string) (restarts, retries int, err error) {
	facts, err := e.Facts.List(ctx, woID)
	if err != nil {
		return 0, 0, err
	}
	for _, f := range facts {
		switch f.Kind {
		case fact.KindWORetried:
			restarts, retries = 0, 0
		case fact.KindSandboxRestarted:
			restarts++
		case fact.KindRunStep:
			if s, derr := fact.Decode[fact.RunStep](f); derr == nil && s.Step == "provider_retry" {
				retries++
			}
		}
	}
	return restarts, retries, nil
}

// overBudget reports the failure to record once transient provider retries have used the work order's budget.
func (e *Engine) overBudget(ctx context.Context, woID string) (fact.WOFailed, bool) {
	_, retries, err := e.budgetUsed(ctx, woID)
	if err != nil || retries < maxTransientRetries {
		return fact.WOFailed{}, false
	}
	return fact.WOFailed{Class: fact.ClassProvider, Reason: "transient_exhausted",
		Message: fmt.Sprintf("the provider failed transiently %d times in this work order", retries)}, true
}

func (e *Engine) retryBase() time.Duration {
	if e.RetryBase > 0 {
		return e.RetryBase
	}
	return defaultRetryBase
}

func (e *Engine) transientTries() int {
	if e.TransientRetries > 0 {
		return e.TransientRetries
	}
	return defaultTransientTries
}

// backoff is base * 2^(attempt-1), capped.
func backoff(base time.Duration, attempt int) time.Duration {
	d := base << (attempt - 1)
	if d <= 0 || d > retryBackoffCap {
		return retryBackoffCap
	}
	return d
}

// verifyTasks maps failing verify output to the done tasks whose owned paths it mentions. A token matches when any
// trailing path of it falls under an owned path, so module-qualified package paths and file:line references both map.
func verifyTasks(output string, tasks []workflow.Task) []string {
	var ids []string
	for _, t := range tasks {
		if t.Status != workflow.TaskDone {
			continue
		}
		for _, tok := range pathTokenRe.FindAllString(output, -1) {
			if mentions(tok, t.Paths) {
				ids = append(ids, t.ID)
				break
			}
		}
	}
	return ids
}

var pathTokenRe = regexp.MustCompile(`[A-Za-z0-9_.@-]+(?:/[A-Za-z0-9_.@-]+)+|[A-Za-z0-9_@-]+\.[A-Za-z0-9]+`)

func mentions(tok string, owned []string) bool {
	var specific []string
	for _, o := range owned {
		if !workflow.Within("no-such-dir/x", []string{o}) { // skip "." and "**": they would claim every failure
			specific = append(specific, o)
		}
	}
	tok = strings.TrimPrefix(tok, "./")
	for {
		if workflow.Owned(tok, specific) {
			return true
		}
		_, rest, ok := strings.Cut(tok, "/")
		if !ok {
			return false
		}
		tok = rest
	}
}

// verifyFailed records the failure and routes it: mapped tasks go back to their builders as failed with the output,
// anything unmapped is escalated.
func (o *orch) verifyFailed(ctx context.Context, cmd, output string) error {
	facts, err := o.e.Facts.List(ctx, o.woID)
	if err != nil {
		return err
	}
	st := workflow.Fold(facts)
	ids := verifyTasks(output, st.Tasks)
	nf := []fact.NewFact{{Kind: fact.KindVerifyFailed, Payload: fact.VerifyFailed{Commands: []string{cmd}, Output: output}}}
	if len(ids) == 0 {
		nf = append(nf, fact.NewFact{Kind: workflow.KindEscalated, Payload: workflow.Escalated{Class: fact.ClassVerify,
			Reason: "verify failed and no failing file maps to a task's owned paths: " + firstLine(output, 300)}})
	}
	for _, id := range ids {
		nf = append(nf, fact.NewFact{Kind: fact.KindTaskFailed, Payload: fact.TaskFailed{Task: id, Class: fact.ClassVerify,
			Reason: "verify_failed", Message: fmt.Sprintf("`%s` failed:\n%s", cmd, output)}})
	}
	_, err = o.e.Facts.AppendBatch(ctx, o.woID, 0, nf)
	return err
}
