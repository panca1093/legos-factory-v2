package usecase

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
)

type envHarness struct{ Harness }

func (envHarness) TakesEnv() bool { return true }

func TestBuilderServicesSkippedWhenHarnessDropsEnv(t *testing.T) {
	ctx := context.Background()
	d := &fakeDocker{}
	f, wo, wos := monitorFactory(t, nil)
	f.Docker, f.Sandbox, f.DataDir = d, &envSandbox{}, t.TempDir()
	prof, _ := json.Marshal(stackProfile())
	if err := wos.UpdateFields(ctx, wo.ID, map[string]any{"check_profile": string(prof)}); err != nil {
		t.Fatal(err)
	}
	wo.CheckProfile = string(prof)
	opts := runOpts{Dir: t.TempDir()}
	line, release := f.builderServices(ctx, wo, model.Job{TaskID: "T1"}, &opts)
	if release != nil || line != "" || opts.Env != nil || len(d.calls) != 0 {
		t.Fatalf("line=%q release=%v env=%v docker=%v", line, release != nil, opts.Env, d.calls)
	}
	var msg string
	for _, e := range agentEvents(t, wos) {
		if e.Kind == "services_unavailable" {
			msg = e.Message
		}
	}
	if !strings.Contains(msg, "harness does not pass env") {
		t.Fatalf("event message %q", msg)
	}
}
