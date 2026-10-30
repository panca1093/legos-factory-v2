package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/harness"
)

func TestLoadProjectExampleLoanPlatform(t *testing.T) {
	p, err := LoadProject(filepath.Join("..", "..", "examples", "loan-platform.json"))
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "loan-platform" || strings.Join(p.Env.Verify, "|") != "make vet-lsm|make test-lsm" || p.Env.Docker ||
		p.Env.TestPkg != "go test ./{pkg}/..." || len(p.ProtectedPaths) == 0 || len(p.RequiredChecks) != 0 || p.Env.Integration != nil {
		t.Fatalf("project = %+v", p)
	}
}

func TestLoadProjectEnvBindingAndFallbacks(t *testing.T) {
	write := func(body string) string {
		path := filepath.Join(t.TempDir(), "p.json")
		if err := os.WriteFile(path, []byte(`{"id":"i","repo_url":"u","repo":"r",`+body+`}`), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	p, err := LoadProject(write(`"setup":"old","verify":["old1"]`))
	if err != nil || p.Env.Setup != "old" || strings.Join(p.Env.Verify, ",") != "old1" {
		t.Fatalf("legacy keys: %+v %v", p.Env, err)
	}
	p, err = LoadProject(write(`"setup":"old","env":{"setup":"new","verify":["a","b"],"docker":true,
		"integration":{"command":"run","endpoints":{"kafka":"k:9092"},"secret_env":["TOK"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	in := p.Env.Integration
	if p.Env.Setup != "new" || len(p.Env.Verify) != 2 || !p.Env.Docker || in == nil || in.Command != "run" ||
		in.Endpoints["kafka"] != "k:9092" || len(in.SecretEnv) != 1 || in.SecretEnv[0] != "TOK" {
		t.Fatalf("env = %+v integration = %+v", p.Env, in)
	}
}

func TestBuilderPromptCarriesTargetedTestCommand(t *testing.T) {
	cases := []struct {
		name, testPkg string
		goMod         bool
		want, notWant string
	}{
		{"configured template", "go test ./services/lsm/{pkg}/...", false, "`go test ./services/lsm/<pkg>/...`", ""},
		{"derived from go.mod", "", true, "`go test ./<pkg>/...`", ""},
		{"nothing to derive", "", false, "without -race. State the exact command", "`"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := newEnv(t)
			ev.p.Env.TestPkg = tc.testPkg
			if tc.goMod {
				ev.p.RepoURL = remoteWithGoMod(t)
			}
			id := ev.create(t)
			if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
				t.Fatal(err)
			}
			prompt := ev.h.prompts[0]
			if !strings.Contains(prompt, "without -race") || !strings.Contains(prompt, "final message") || !strings.Contains(prompt, tc.want) {
				t.Fatalf("prompt = %q", prompt)
			}
			if tc.notWant != "" && strings.Contains(prompt[strings.Index(prompt, "Before finishing"):], tc.notWant) {
				t.Fatalf("prompt names a command: %q", prompt)
			}
		})
	}
}

func remoteWithGoMod(t *testing.T) string {
	t.Helper()
	bare := newRemote(t)
	clone := t.TempDir()
	sh(t, clone, "git", "clone", "-q", bare, "c")
	c := filepath.Join(clone, "c")
	if err := os.WriteFile(filepath.Join(c, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh(t, c, "git", "add", "-A")
	sh(t, c, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "gomod")
	sh(t, c, "git", "push", "-q", "origin", "main")
	return bare
}

func TestNoTargetedTestsWarning(t *testing.T) {
	for _, tc := range []struct {
		tool string
		want int
	}{{"", 1}, {"go test ./pkg/...", 0}} {
		ev := newEnv(t)
		ev.h.toolText = tc.tool
		id := ev.create(t)
		if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, f := range mustList(t, ev, id) {
			if f.Kind == fact.KindRunStep {
				if s, _ := fact.Decode[fact.RunStep](f); s.Step == "no_targeted_tests" {
					n++
				}
			}
		}
		if n != tc.want {
			t.Errorf("tool %q: %d warnings, want %d", tc.tool, n, tc.want)
		}
		if st := status(t, ev.e, id); st != fact.StatusPROpen {
			t.Errorf("tool %q: warning must not fail the order, status = %s", tc.tool, st)
		}
	}
}

func verifyFailedFacts(t *testing.T, ev *env, id string) []fact.VerifyFailed {
	t.Helper()
	var out []fact.VerifyFailed
	for _, f := range mustList(t, ev, id) {
		if f.Kind == fact.KindVerifyFailed {
			v, _ := fact.Decode[fact.VerifyFailed](f)
			out = append(out, v)
		}
	}
	return out
}

func TestIntegrationStepEnvAndSecrets(t *testing.T) {
	t.Setenv("LF_TEST_SECRET", "tok-123")
	ev := newEnv(t, "echo verify-ran >> ../order.log")
	ev.p.Env.Integration = &Integration{
		Command:   `echo "$LF_ENDPOINT_KAFKA $LF_TEST_SECRET" > "$HOME/integration.out"; echo "$LF_TEST_SECRET leaked"; exit 1`,
		Endpoints: map[string]string{"kafka": "k:9092"},
		SecretEnv: []string{"LF_TEST_SECRET"},
	}
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusFailed || failedClass(t, ev.e, id) != fact.ClassVerify {
		t.Fatalf("status = %s class = %s", st, failedClass(t, ev.e, id))
	}
	vf := verifyFailedFacts(t, ev, id)
	if len(vf) != 1 || vf[0].Tier != "integration" {
		t.Fatalf("verify.failed = %+v", vf)
	}
	for _, f := range mustList(t, ev, id) {
		if strings.Contains(string(f.Payload), "tok-123") {
			t.Fatalf("secret in fact %s: %s", f.Kind, f.Payload)
		}
	}
	if !strings.Contains(vf[0].Output, "*** leaked") {
		t.Errorf("output = %q", vf[0].Output)
	}
	home := filepath.Join(ev.root, id, "home", "integration.out")
	if b, err := os.ReadFile(home); err != nil || strings.TrimSpace(string(b)) != "k:9092 tok-123" {
		t.Errorf("integration env: %q %v", b, err)
	}
}

func TestIntegrationSkippedWhenVerifyFailsAndPassesToPR(t *testing.T) {
	ev := newEnv(t, "exit 1")
	ev.p.Env.Integration = &Integration{Command: "touch ../integration-ran"}
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if vf := verifyFailedFacts(t, ev, id); len(vf) != 1 || vf[0].Tier != "verify" {
		t.Fatalf("verify.failed = %+v", vf)
	}
	if _, err := os.Stat(filepath.Join(ev.root, id, "integration-ran")); err == nil {
		t.Error("integration ran after verify failed")
	}

	ev = newEnv(t, "true")
	ev.p.Env.Integration = &Integration{Command: "true"}
	id = ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s", st)
	}
}

func TestIntegrationMissingSecretFailsClearly(t *testing.T) {
	ev := newEnv(t, "true")
	ev.p.Env.Integration = &Integration{Command: "true", SecretEnv: []string{"LF_DEFINITELY_UNSET"}}
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusFailed {
		t.Fatalf("status = %s", st)
	}
}

// A failing file owned by task B goes back to B, whichever tier reported it.
func TestVerifyFailureMapsToOwningTaskPerTier(t *testing.T) {
	for _, tier := range []string{"verify", "integration"} {
		t.Run(tier, func(t *testing.T) {
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
			ev := lineEnv(t, h, "true")
			if tier == "verify" {
				ev.p.Env.Verify = []string{verifyCmd}
			} else {
				ev.p.Env.Integration = &Integration{Command: verifyCmd}
			}
			id := ev.create(t)
			if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
				t.Fatal(err)
			}
			if st := status(t, ev.e, id); st != fact.StatusPROpen {
				t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, id))
			}
			var failed []fact.TaskFailed
			for _, f := range mustList(t, ev, id) {
				if f.Kind == fact.KindTaskFailed {
					v, _ := fact.Decode[fact.TaskFailed](f)
					failed = append(failed, v)
				}
			}
			if len(failed) != 1 || failed[0].Task != "b" || failed[0].Class != fact.ClassVerify ||
				!strings.Contains(failed[0].Message, "("+tier+")") || !strings.Contains(failed[0].Message, "b/f_test.go:3: boom") {
				t.Fatalf("task.failed = %+v", failed)
			}
			if vf := verifyFailedFacts(t, ev, id); len(vf) != 1 || vf[0].Tier != tier {
				t.Fatalf("verify.failed = %+v", vf)
			}
		})
	}
}
