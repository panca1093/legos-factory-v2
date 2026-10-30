//go:build docker

package sandbox

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestDockerLifecycle(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	image := os.Getenv("SANDBOX_TEST_IMAGE")
	if image == "" {
		image = "alpine:3.20"
	}
	ctx := context.Background()
	d := Docker{Image: image, CPUs: 1, MemoryBytes: 256 << 20}
	const id = "dockertest"

	ws, err := d.CreateWorkspace(ctx, id)
	if err != nil {
		t.Skipf("docker daemon unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.DeleteWorkspace(ctx, id) })
	if _, err := d.CreateWorkspace(ctx, id); err != nil {
		t.Fatalf("not idempotent: %v", err)
	}

	sb, err := d.Start(ctx, ws, map[string]string{"FOO": "bar"})
	if err != nil {
		t.Fatal(err)
	}
	out, _, code, err := sb.Exec(ctx, ws.Root(), map[string]string{"X": "y"}, "sh", "-c", "echo hi $FOO$X $HOME && touch /ws/marker")
	if err != nil || code != 0 || strings.TrimSpace(out) != "hi bary /ws/home" {
		t.Fatalf("out=%q code=%d err=%v", out, code, err)
	}
	if err := sb.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	if err := exec.Command("docker", "volume", "inspect", volumeName(id)).Run(); err != nil {
		t.Fatalf("volume gone after stop: %v", err)
	}
	sb2, err := d.Start(ctx, ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, code, _ := sb2.Exec(ctx, "", nil, "test", "-f", "/ws/marker"); code != 0 {
		t.Fatal("marker not persisted on volume")
	}
	if err := sb2.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	if err := d.DeleteWorkspace(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("docker", "volume", "inspect", volumeName(id)).Run(); err == nil {
		t.Fatal("volume still exists after delete")
	}
}
