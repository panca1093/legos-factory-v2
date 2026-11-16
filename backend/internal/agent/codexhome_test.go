package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveRotatedLoginKeepsANewerAccountFile(t *testing.T) {
	src := t.TempDir()
	auth := filepath.Join(src, "auth.json")
	_ = os.WriteFile(auth, []byte("v1"), 0o600)
	saveRotatedLogin(src, auth, []byte("v1"), []byte("v2"))
	if b, _ := os.ReadFile(auth); string(b) != "v2" {
		t.Fatalf("unchanged account file must take the rotated login, got %q", b)
	}
	// a second run started from v1 and finishes after v2 was saved: it must not overwrite the newer file
	saveRotatedLogin(src, auth, []byte("v1"), []byte("v1b"))
	if b, _ := os.ReadFile(auth); string(b) != "v2" {
		t.Fatalf("newer account file overwritten: %q", b)
	}
}

func TestOnlyClaudeTakesMCP(t *testing.T) {
	for p, want := range map[string]bool{"claude": true, "codex": false, "opencode": false} {
		if got := (&ACPHarness{Provider: p}).SupportsMCP(); got != want {
			t.Fatalf("%s: %v", p, got)
		}
	}
}
