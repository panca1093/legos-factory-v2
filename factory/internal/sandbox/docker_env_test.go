package sandbox

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestDockerCommandKeepsEnvValuesOutOfArgv(t *testing.T) {
	s := &dockerSandbox{d: Docker{}, id: "cid"}
	cmd := s.Command("/ws", map[string]string{"API_KEY": "s3cret"}, "agent")
	for _, a := range cmd.Argv {
		if strings.Contains(a, "s3cret") {
			t.Fatalf("secret in argv: %v", cmd.Argv)
		}
	}
	if !reflect.DeepEqual(cmd.Argv, []string{"docker", "exec", "-i", "-w", "/ws", "-e", "API_KEY", "cid", "agent"}) {
		t.Fatalf("argv=%v", cmd.Argv)
	}
	if !strings.Contains(strings.Join(cmd.Env, "\n"), "API_KEY=s3cret") {
		t.Fatal("value missing from Cmd.Env")
	}
}

func TestDockerRunArgsMountSocketOnlyWhenRequested(t *testing.T) {
	d := Docker{Image: "img"}
	const mount = "/var/run/docker.sock:/var/run/docker.sock"
	if args := d.runArgs(Workspace{ID: "w", root: "/ws"}, nil); slices.Contains(args, mount) {
		t.Fatalf("socket mounted by default: %v", args)
	}
	if args := d.runArgs(Workspace{ID: "w", root: "/ws", Docker: true}, nil); !slices.Contains(args, mount) {
		t.Fatalf("socket not mounted: %v", args)
	}
}
