package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/harness"
	"legos-factory/factory/internal/profile"
	"legos-factory/factory/internal/sandbox"
	"legos-factory/factory/internal/workflow"
)

func TestParallelBuildersRunConcurrentlyAndBothLand(t *testing.T) {
	h := newScripted()
	var arrived sync.WaitGroup
	arrived.Add(2)
	barrier := make(chan struct{})
	go func() { arrived.Wait(); close(barrier) }()
	foremanAsked := make(chan struct{})
	var foremanCalls atomic.Int32
	h.onRole = func(s harness.SessionSpec) {
		if s.Role == harness.RoleForeman && foremanCalls.Add(1) == 4 {
			close(foremanAsked)
		}
	}
	h.build = func(task string, _ int, dir string) error {
		arrived.Done()
		select {
		case <-barrier:
		case <-time.After(10 * time.Second):
			return errors.New("builders did not overlap")
		}
		if err := writeIn(task+"/f.txt", task+"\n")(task, 0, dir); err != nil {
			return err
		}
		if task == "b" { // b is still building when the Foreman is asked about a
			select {
			case <-foremanAsked:
			case <-time.After(10 * time.Second):
				return errors.New("Foreman was not asked while b was building")
			}
		}
		return nil
	}
	h.say(harness.RoleArchitect, "spec", planJSON(taskA+","+taskB))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), call("dispatch_build", `{"task_ids":["a","b"]}`),
		call("request_inspection", `{"task_id":"a"}`), call("request_inspection", `{"task_id":"b"}`))
	ev := lineEnv(t, h)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, id))
	}
	for _, f := range []string{"a/f.txt", "b/f.txt"} {
		if got := sh(t, ev.p.RepoURL, "git", "show", "lf/"+id+":"+f); got != filepath.Dir(f) {
			t.Errorf("%s = %q", f, got)
		}
	}
	ports := map[string]string{}
	for _, s := range h.specs {
		if s.Role == harness.RoleBuilder {
			p := s.Env["PORT"]
			if p == "" || ports[p] != "" {
				t.Errorf("PORT %q reused or empty: %v", p, ports)
			}
			ports[p] = filepath.Base(s.Dir)
		}
	}
	if len(ports) != 2 {
		t.Errorf("ports = %v", ports)
	}
	if p, err := ev.e.ports().Acquire("probe"); err != nil || p != 30000 {
		t.Errorf("ports not released after the tasks: %d %v", p, err)
	}
}

func TestParallelBuilderFailureDoesNotWaitForSibling(t *testing.T) {
	h := newScripted()
	release := make(chan struct{})
	h.build = func(task string, _ int, dir string) error {
		if task == "b" {
			select {
			case <-release:
			case <-time.After(10 * time.Second):
				return errors.New("Foreman was not asked while b was building")
			}
			return writeIn("b/f.txt", "b\n")(task, 0, dir)
		}
		return nil // a produces nothing and fails fast
	}
	var asked atomic.Int32
	h.onRole = func(s harness.SessionSpec) {
		if s.Role == harness.RoleForeman && asked.Add(1) == 4 {
			close(release)
		}
	}
	h.say(harness.RoleArchitect, "spec", planJSON(taskA+","+taskB))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), call("dispatch_build", `{"task_ids":["a","b"]}`),
		call("escalate_to_human", `{"reason":"a failed"}`))
	ev := lineEnv(t, h)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusAwaitingHuman {
		t.Fatalf("status = %s", st)
	}
	if !strings.Contains(h.promptsFor(harness.RoleForeman)[3], "a[failed]") {
		t.Errorf("Foreman not told a failed: %q", h.promptsFor(harness.RoleForeman)[3])
	}
}

func TestClassify(t *testing.T) {
	transient := &harness.ProviderError{IsTransient: true, Message: "503"}
	permanent := &harness.ProviderError{Message: "insufficient funds"}
	cases := []struct {
		name string
		err  error
		down bool
		want Failure
	}{
		{"nil", nil, false, Failure{}},
		{"canceled", context.Canceled, true, Failure{}},
		{"transient provider", transient, false, Failure{Class: fact.ClassProvider, Transient: true}},
		{"wrapped transient provider", fmt.Errorf("prompt: %w", transient), true, Failure{Class: fact.ClassProvider, Transient: true}},
		{"permanent provider", permanent, false, Failure{Class: fact.ClassProvider}},
		{"sandbox down", errors.New("exec: container exited"), true, Failure{Class: fact.ClassInfra, Reason: "sandbox_down"}},
		{"classed infra", classed(fact.ClassInfra, "sandbox_down", errors.New("oom")), false, Failure{Class: fact.ClassInfra, Reason: "sandbox_down"}},
		{"classed integration", fmt.Errorf("wrap: %w", classed(fact.ClassIntegration, "conflict", errors.New("x"))), false, Failure{Class: fact.ClassIntegration, Reason: "conflict"}},
		{"classed verify", classed(fact.ClassVerify, "verify_failed", errors.New("x")), false, Failure{Class: fact.ClassVerify, Reason: "verify_failed"}},
		{"classed foreman", classed(fact.ClassForeman, "rejections", errors.New("x")), false, Failure{Class: fact.ClassForeman, Reason: "rejections"}},
		{"other error with healthy sandbox", errors.New("agent exited"), false, Failure{Class: fact.ClassTask, Reason: "error"}},
	}
	for _, tc := range cases {
		if got := Classify(tc.err, tc.down); got != tc.want {
			t.Errorf("%s: Classify = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestBackoffDoublesAndCaps(t *testing.T) {
	base := time.Second
	for attempt, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 12: retryBackoffCap, 80: retryBackoffCap} {
		if got := backoff(base, attempt); got != want {
			t.Errorf("backoff(%d) = %s, want %s", attempt, got, want)
		}
	}
}

func TestVerifyTasksMapsFailingFilesToOwners(t *testing.T) {
	done := func(id string, paths ...string) workflow.Task {
		return workflow.Task{PlanTask: workflow.PlanTask{ID: id, Paths: paths}, Status: workflow.TaskDone}
	}
	tasks := []workflow.Task{done("a", "svc/a/"), done("b", "svc/b/", "docs/x.md"), done("all", ".")}
	cases := []struct {
		name, out string
		want      []string
	}{
		{"file and line", "--- FAIL: TestX\n    svc/b/x_test.go:12: boom", []string{"b"}},
		{"module qualified package", "FAIL\texample.com/m/svc/a\t0.4s", []string{"a"}},
		{"two owners", "svc/a/a.go:1: x\nsvc/b/b.go:2: y", []string{"a", "b"}},
		{"unmapped", "exit status 1\nsomething broke", nil},
		{"sibling prefix is not ownership", "svc/ab/z.go:3: nope", nil},
		{"owned single file", "docs/x.md:1 bad link", []string{"b"}},
	}
	for _, tc := range cases {
		got := verifyTasks(tc.out, tasks)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	notDone := []workflow.Task{{PlanTask: workflow.PlanTask{ID: "a", Paths: []string{"svc/a/"}}, Status: workflow.TaskFailed}}
	if got := verifyTasks("svc/a/a.go:1", notDone); got != nil {
		t.Errorf("failed task mapped: %v", got)
	}
}

const taskBAfterA = `{"id":"b","description":"build b","owned_paths":["b/"],"depends_on":["a"]}`

const verifyCmd = `if grep -q ok b/f.txt; then exit 0; fi; echo "FAIL b/f_test.go:3: boom"; exit 1`

func sequentialForeman(extra ...string) []string {
	return append([]string{call("approve_spec", `{}`), call("approve_plan", `{}`),
		call("dispatch_build", `{"task_ids":["a"]}`), call("request_inspection", `{"task_id":"a"}`),
		call("dispatch_build", `{"task_ids":["b"]}`), call("request_inspection", `{"task_id":"b"}`)}, extra...)
}

func TestVerifyFailureGoesBackToOwningBuilder(t *testing.T) {
	h := newScripted()
	h.build = func(task string, n int, dir string) error {
		content := task
		if task == "b" {
			content = map[bool]string{true: "bad", false: "ok"}[n == 1]
		}
		return writeIn(task+"/f.txt", content+"\n")(task, n, dir)
	}
	h.say(harness.RoleArchitect, "spec", planJSON(taskA+","+taskBAfterA))
	h.say(harness.RoleForeman, sequentialForeman(call("retry_task", `{"task_id":"b","change":"same"}`),
		call("dispatch_build", `{"task_ids":["b"]}`), call("request_inspection", `{"task_id":"b"}`))...)
	ev := lineEnv(t, h, verifyCmd)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		for _, f := range mustList(t, ev, id) {
			t.Logf("%s %s", f.Kind, firstLine(string(f.Payload), 200))
		}
		t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, id))
	}
	var failed []fact.TaskFailed
	for _, f := range mustList(t, ev, id) {
		if f.Kind == fact.KindTaskFailed {
			v, _ := fact.Decode[fact.TaskFailed](f)
			failed = append(failed, v)
		}
	}
	if len(failed) != 1 || failed[0].Task != "b" || failed[0].Class != fact.ClassVerify || !strings.Contains(failed[0].Message, "b/f_test.go:3: boom") {
		t.Fatalf("task.failed = %+v", failed)
	}
	bp := h.promptsFor(harness.RoleBuilder)
	if len(bp) != 3 || !strings.Contains(bp[2], "b/f_test.go:3: boom") {
		t.Errorf("builder prompts = %q", bp)
	}
	if k := ev.kinds(t, ev.e, id); count(k, fact.KindVerifyFailed) != 1 || count(k, fact.KindVerifyPassed) != 1 {
		t.Errorf("kinds = %v", k)
	}
	if got := sh(t, ev.p.RepoURL, "git", "show", "lf/"+id+":b/f.txt"); got != "ok" {
		t.Errorf("b/f.txt = %q", got)
	}
}

func TestUnmappedVerifyFailureEscalates(t *testing.T) {
	h := newScripted()
	h.build = func(task string, n int, dir string) error { return writeIn(task+"/f.txt", task+"\n")(task, n, dir) }
	h.say(harness.RoleArchitect, "spec", planJSON(taskA+","+taskBAfterA))
	h.say(harness.RoleForeman, sequentialForeman()...)
	ev := lineEnv(t, h, `echo "mystery failure"; exit 1`)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusAwaitingHuman {
		t.Fatalf("status = %s", st)
	}
	var esc workflow.Escalated
	for _, f := range mustList(t, ev, id) {
		if f.Kind == workflow.KindEscalated {
			esc, _ = fact.Decode[workflow.Escalated](f)
		}
	}
	if esc.Class != fact.ClassVerify || !strings.Contains(esc.Reason, "mystery failure") {
		t.Errorf("escalated = %+v", esc)
	}
}

func TestConsecutiveRejectionsEscalateAsForemanClass(t *testing.T) {
	h := newScripted()
	h.say(harness.RoleArchitect, "spec")
	h.say(harness.RoleForeman, "x", "y", "z")
	ev := lineEnv(t, h)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	for _, f := range mustList(t, ev, id) {
		if f.Kind == workflow.KindEscalated {
			if e, _ := fact.Decode[workflow.Escalated](f); e.Class != fact.ClassForeman {
				t.Errorf("escalated = %+v", e)
			}
			return
		}
	}
	t.Fatal("no escalation")
}

func providerErr(transient bool) error {
	return &harness.ProviderError{IsTransient: transient, Message: map[bool]string{true: "overloaded 529", false: "insufficient funds"}[transient]}
}

func TestTransientProviderFailureRetriesWithBackoffOutsideTaskBudget(t *testing.T) {
	h := newScripted()
	h.build = writeIn("pkg/out.txt", "built\n")
	h.inject = func(role harness.Role, _ string, n int) error {
		if role == harness.RoleBuilder && n <= 2 {
			return providerErr(true)
		}
		return nil
	}
	h.say(harness.RoleArchitect, "spec", planJSON(`{"id":"t1","description":"d","owned_paths":["pkg/"]}`))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), call("dispatch_build", `{"task_ids":["t1"]}`),
		call("request_inspection", `{"task_id":"t1"}`))
	ev := lineEnv(t, h)
	ev.e.RetryBase = time.Millisecond
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, id))
	}
	k := ev.kinds(t, ev.e, id)
	if count(k, workflow.KindTaskRetry) != 0 || count(k, fact.KindTaskFailed) != 0 {
		t.Errorf("transient failure consumed the task budget: %v", k)
	}
	retries := 0
	for _, f := range mustList(t, ev, id) {
		if f.Kind == fact.KindRunStep {
			if s, _ := fact.Decode[fact.RunStep](f); s.Step == "provider_retry" {
				retries++
			}
		}
		if f.Kind == fact.KindRunFinished {
			if r, _ := fact.Decode[fact.RunFinished](f); r.StopReason == "error" {
				t.Errorf("transient failure recorded as a permanent error: %+v", r)
			}
		}
	}
	if retries != 2 {
		t.Errorf("provider_retry steps = %d, want 2", retries)
	}
}

func TestTransientRetriesAreBounded(t *testing.T) {
	h := newScripted()
	h.inject = func(role harness.Role, _ string, _ int) error {
		if role == harness.RoleArchitect {
			return providerErr(true)
		}
		return nil
	}
	ev := lineEnv(t, h)
	ev.e.RetryBase, ev.e.TransientRetries = time.Millisecond, 2
	id := ev.create(t)
	err := ev.e.Run(context.Background(), ev.p, id)
	if !IsTransient(err) {
		t.Fatalf("err = %v, want the transient error after the retries", err)
	}
	if n := len(h.promptsFor(harness.RoleArchitect)); n != 3 {
		t.Errorf("architect prompted %d times, want 1 + 2 retries", n)
	}
}

func TestPermanentProviderFailureFailsOnceWithModelAndProvider(t *testing.T) {
	h := newScripted()
	h.inject = func(role harness.Role, _ string, _ int) error {
		if role == harness.RoleBuilder {
			return providerErr(false)
		}
		return nil
	}
	h.say(harness.RoleArchitect, "spec", planJSON(`{"id":"t1","description":"d","owned_paths":["pkg/"]}`))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), call("dispatch_build", `{"task_ids":["t1"]}`))
	ev := lineEnv(t, h)
	ev.p.Builder = ev.p.Roles["foreman"]
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	var wf fact.WOFailed
	n := 0
	for _, f := range mustList(t, ev, id) {
		if f.Kind == fact.KindWOFailed {
			wf, _ = fact.Decode[fact.WOFailed](f)
			n++
		}
	}
	if n != 1 || wf.Class != fact.ClassProvider || wf.Model != "opus" || !strings.Contains(wf.Message, "insufficient funds") {
		t.Errorf("wo.failed x%d = %+v", n, wf)
	}
	if len(h.promptsFor(harness.RoleBuilder)) != 1 {
		t.Errorf("permanent failure was retried: %d builder prompts", len(h.promptsFor(harness.RoleBuilder)))
	}
}

// crashRuntime wraps sandboxes whose Exec fails once killed, like a container that was OOM-killed.
type crashRuntime struct {
	sandbox.Runtime
	mu    sync.Mutex
	boxes []*crashSandbox
}

type crashSandbox struct {
	sandbox.Sandbox
	dead atomic.Bool
}

func (c *crashSandbox) Exec(ctx context.Context, dir string, env map[string]string, args ...string) (string, string, int, error) {
	if c.dead.Load() {
		return "", "", -1, errors.New("container exited")
	}
	return c.Sandbox.Exec(ctx, dir, env, args...)
}

func (r *crashRuntime) Start(ctx context.Context, ws sandbox.Workspace, env map[string]string) (sandbox.Sandbox, error) {
	sb, err := r.Runtime.Start(ctx, ws, env)
	if err != nil {
		return nil, err
	}
	cs := &crashSandbox{Sandbox: sb}
	r.mu.Lock()
	r.boxes = append(r.boxes, cs)
	r.mu.Unlock()
	return cs, nil
}

func (r *crashRuntime) killLatest() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.boxes[len(r.boxes)-1].dead.Store(true)
}

func (r *crashRuntime) started() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.boxes) }

func TestInfraCrashRestartsSandboxAndResumesFromFacts(t *testing.T) {
	h := newScripted()
	rt := &crashRuntime{}
	h.build = func(task string, n int, dir string) error {
		if n == 1 {
			rt.killLatest()
		}
		return writeIn("pkg/out.txt", "built\n")(task, n, dir)
	}
	h.say(harness.RoleArchitect, "spec", planJSON(`{"id":"t1","description":"d","owned_paths":["pkg/"]}`))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), call("dispatch_build", `{"task_ids":["t1"]}`),
		call("request_inspection", `{"task_id":"t1"}`))
	ev := lineEnv(t, h)
	rt.Runtime = ev.e.Runtime
	ev.e.Runtime = rt
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, id))
	}
	k := ev.kinds(t, ev.e, id)
	if count(k, fact.KindSandboxRestarted) != 1 || rt.started() != 2 {
		t.Errorf("restarts = %d, sandboxes = %d", count(k, fact.KindSandboxRestarted), rt.started())
	}
	if n := len(h.promptsFor(harness.RoleArchitect)); n != 2 {
		t.Errorf("architect re-ran after the crash: %d prompts", n)
	}
	if acc, _ := decisions(t, ev, id); len(acc) != 4 {
		t.Errorf("Foreman decisions replayed after the crash: %v", acc)
	}
}

func TestInfraRestartsAreBounded(t *testing.T) {
	h := newScripted()
	rt := &crashRuntime{}
	h.build = func(string, int, string) error { rt.killLatest(); return nil }
	h.say(harness.RoleArchitect, "spec", planJSON(`{"id":"t1","description":"d","owned_paths":["pkg/"]}`))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), call("dispatch_build", `{"task_ids":["t1"]}`))
	ev := lineEnv(t, h)
	rt.Runtime = ev.e.Runtime
	ev.e.Runtime = rt
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if c := failedClass(t, ev.e, id); c != fact.ClassInfra {
		t.Fatalf("class = %q", c)
	}
	if rt.started() != 1+maxInfraRestarts {
		t.Errorf("sandboxes = %d", rt.started())
	}
}

func fallbackEnv(t *testing.T, h *scripted) (*env, *fakeProfiles) {
	ev := lineEnv(t, h)
	ev.p.Builder = BuilderConfig{Profile: harness.Profile{Agent: "claude-agent-acp", Model: "primary"}, AgentCommand: []string{"claude-agent-acp"}}
	fp := &fakeProfiles{byID: map[string]profile.Profile{
		"fb1": {ID: "fb1", Agent: "claude-agent-acp", CredentialID: "c", Model: "fallback-1"},
	}}
	ev.e.Profiles = fp
	ev.p.Fallbacks = map[string][]string{"builder": {"fb1"}}
	return ev, fp
}

func TestRetryOtherModelUsesFallbackProfileAndIsRejectedWhenNoneLeft(t *testing.T) {
	h := newScripted()
	h.build = func(task string, n int, dir string) error {
		if n < 3 {
			return nil // no changes
		}
		return writeIn("pkg/out.txt", "built\n")(task, n, dir)
	}
	h.say(harness.RoleArchitect, "spec", planJSON(`{"id":"t1","description":"d","owned_paths":["pkg/"]}`))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), call("dispatch_build", `{"task_ids":["t1"]}`),
		call("retry_task", `{"task_id":"t1","change":"other_model"}`), call("dispatch_build", `{"task_ids":["t1"]}`),
		call("retry_task", `{"task_id":"t1","change":"other_model"}`), // no second fallback: rejected
		call("retry_task", `{"task_id":"t1","change":"same"}`), call("dispatch_build", `{"task_ids":["t1"]}`),
		call("request_inspection", `{"task_id":"t1"}`))
	ev, fp := fallbackEnv(t, h)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, id))
	}
	var models []string
	for _, s := range h.specs {
		if s.Role == harness.RoleBuilder {
			models = append(models, s.Profile.Model)
		}
	}
	if strings.Join(models, ",") != "primary,fallback-1,fallback-1" {
		t.Errorf("builder models = %v", models)
	}
	if _, rej := decisions(t, ev, id); len(rej) != 1 || rej[0] != "retry_task" {
		t.Errorf("rejected = %v", rej)
	}
	if fp.acquired != fp.released {
		t.Errorf("credential slots leaked: %d/%d", fp.acquired, fp.released)
	}
	var tf fact.TaskFailed
	for _, f := range mustList(t, ev, id) {
		if f.Kind == fact.KindTaskFailed {
			tf, _ = fact.Decode[fact.TaskFailed](f)
			break
		}
	}
	if tf.Class != fact.ClassTask || tf.Reason != "no_changes" || tf.Model != "primary" {
		t.Errorf("task.failed = %+v", tf)
	}
}

func TestRetrySplitReplacesTaskWithArchitectSubPlan(t *testing.T) {
	h := newScripted()
	h.build = func(task string, n int, dir string) error {
		if task == "t1" {
			return nil
		}
		return writeIn("pkg/"+task+"/f.txt", task+"\n")(task, n, dir)
	}
	sub := planJSON(`{"id":"t1a","description":"half a","owned_paths":["pkg/t1a/"]},{"id":"t1b","description":"half b","owned_paths":["pkg/t1b/"],"depends_on":["t1a"]}`)
	h.say(harness.RoleArchitect, "spec", planJSON(`{"id":"t1","description":"big","owned_paths":["pkg/"]}`), sub)
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), call("dispatch_build", `{"task_ids":["t1"]}`),
		call("retry_task", `{"task_id":"t1","change":"split"}`),
		call("dispatch_build", `{"task_ids":["t1a"]}`), call("request_inspection", `{"task_id":"t1a"}`),
		call("dispatch_build", `{"task_ids":["t1b"]}`), call("request_inspection", `{"task_id":"t1b"}`))
	ev := lineEnv(t, h)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, id))
	}
	facts := mustList(t, ev, id)
	st := workflow.Fold(facts)
	var ids []string
	for _, tk := range st.Tasks {
		ids = append(ids, tk.ID)
	}
	if strings.Join(ids, ",") != "t1a,t1b" {
		t.Errorf("plan = %v", ids)
	}
	if count(ev.kinds(t, ev.e, id), workflow.KindPlanSplit) != 1 {
		t.Errorf("plan.split missing")
	}
	for _, f := range []string{"pkg/t1a/f.txt", "pkg/t1b/f.txt"} {
		sh(t, ev.p.RepoURL, "git", "show", "lf/"+id+":"+f)
	}
	if ap := h.promptsFor(harness.RoleArchitect); !strings.Contains(ap[len(ap)-1], "Task \"t1\"") || !strings.Contains(ap[len(ap)-1], "within [pkg]") {
		t.Errorf("split prompt = %q", ap[len(ap)-1])
	}
}

func TestSplitOutsideParentPathsFailsTaskAgain(t *testing.T) {
	h := newScripted()
	h.build = func(string, int, string) error { return nil }
	bad := planJSON(`{"id":"x1","description":"x","owned_paths":["pkg/a/"]},{"id":"x2","description":"y","owned_paths":["other/"]}`)
	h.say(harness.RoleArchitect, "spec", planJSON(`{"id":"t1","description":"big","owned_paths":["pkg/"]}`), bad, bad)
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), call("dispatch_build", `{"task_ids":["t1"]}`),
		call("retry_task", `{"task_id":"t1","change":"split"}`), call("escalate_to_human", `{"reason":"give up"}`))
	ev := lineEnv(t, h)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	var last fact.TaskFailed
	for _, f := range mustList(t, ev, id) {
		if f.Kind == fact.KindTaskFailed {
			last, _ = fact.Decode[fact.TaskFailed](f)
		}
	}
	if last.Reason != "split_failed" || !strings.Contains(last.Message, "not within") {
		t.Errorf("task.failed = %+v", last)
	}
	if count(ev.kinds(t, ev.e, id), workflow.KindPlanSplit) != 0 {
		t.Error("an invalid split was recorded")
	}
	if tk, _ := workflow.Fold(mustList(t, ev, id)).Task("t1"); tk.SplitPending || tk.Status != workflow.TaskFailed {
		t.Errorf("t1 = %+v", tk)
	}
}
