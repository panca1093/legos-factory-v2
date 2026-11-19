package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

// TestHelperFakeLSP is the fake language server: the tests launch the test binary itself through a wrapper script.
func TestHelperFakeLSP(t *testing.T) {
	if os.Getenv("LEGOS_FAKE_LSP") == "" {
		return
	}
	r := bufio.NewReader(os.Stdin)
	for {
		raw, err := ReadFrame(r)
		if err != nil {
			os.Exit(0)
		}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(raw, &m)
		reply := func(result any) {
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
			_ = WriteFrame(os.Stdout, b)
		}
		switch m.Method {
		case "initialize", "echo":
			reply(json.RawMessage(m.Params))
		case "env":
			reply(os.Environ())
		case "cwd":
			wd, _ := os.Getwd()
			reply(wd)
		case "stderr":
			fmt.Fprint(os.Stderr, strings.Repeat("e", 6000)+"END")
			reply(nil)
		case "crash":
			os.Exit(3)
		case "spawnchild":
			c := exec.Command("sleep", "300")
			_ = c.Start()
			reply(c.Process.Pid)
		case "shutdown":
			reply(nil)
		case "exit":
			os.Exit(0)
		}
	}
}

func fakeServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "gopls")
	script := fmt.Sprintf("#!/bin/sh\nLEGOS_FAKE_LSP=1 exec %q -test.run='^TestHelperFakeLSP$'\n", os.Args[0])
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func goplsSpec() model.LSPServerSpec { s, _ := LSPSpecByID("gopls"); return s }

func request(t *testing.T, p model.LSPProcess, id int, method string, params any) json.RawMessage {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err := p.WriteMessage(b); err != nil {
		t.Fatal(err)
	}
	raw, err := p.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v (stderr %q)", err, p.StderrTail())
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
	}
	_ = json.Unmarshal(raw, &resp)
	return resp.Result
}

func TestFramingRoundTripWithMultiByteContent(t *testing.T) {
	msg := []byte(`{"jsonrpc":"2.0","method":"x","params":{"s":"héllo 世界 😀"}}`)
	var buf bytes.Buffer
	if err := WriteFrame(&buf, msg); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(buf.String(), "Content-Length: "+strconv.Itoa(len(msg))+"\r\n\r\n") {
		t.Fatalf("header counts bytes, not runes: %q", buf.String()[:30])
	}
	got, err := ReadFrame(bufio.NewReader(&buf))
	if err != nil || !bytes.Equal(got, msg) {
		t.Fatalf("round trip: %v %q", err, got)
	}
}

func TestReadFrameRejectsOversizeAndMissingLength(t *testing.T) {
	if _, err := ReadFrame(bufio.NewReader(strings.NewReader(fmt.Sprintf("Content-Length: %d\r\n\r\n", MaxLSPFrame+1)))); err == nil {
		t.Fatal("oversize frame accepted")
	}
	if _, err := ReadFrame(bufio.NewReader(strings.NewReader("X-Other: 1\r\n\r\n{}"))); err == nil {
		t.Fatal("frame without Content-Length accepted")
	}
	got, err := ReadFrame(bufio.NewReader(strings.NewReader("content-length: 2\r\nContent-Type: x\r\n\r\n{}")))
	if err != nil || string(got) != "{}" {
		t.Fatalf("case-insensitive header with extra header: %v %q", err, got)
	}
}

func TestStartSpeaksFramedJSONRPCInTheFolder(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	h := &LSPHost{FixedDirs: []string{}, GoEnv: func(context.Context) map[string]string { return nil }, NPMPrefix: func(context.Context) string { return "" }}
	p, err := h.Start(context.Background(), goplsSpec(), fakeServer(t), root)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	var cwd string
	_ = json.Unmarshal(request(t, p, 1, "cwd", nil), &cwd)
	if cwd != root {
		t.Fatalf("cwd %q want %q", cwd, root)
	}
	if got := request(t, p, 2, "echo", map[string]string{"s": "世界"}); string(got) != `{"s":"世界"}` {
		t.Fatalf("echo %s", got)
	}
}

func TestStartScrubsEnvironment(t *testing.T) {
	secrets := map[string]string{"FACTORY_SECRET_KEY": "a", "GH_TOKEN": "b", "GITHUB_TOKEN": "c", "ANTHROPIC_API_KEY": "d", "OPENAI_API_KEY": "e", "TYPESAFE_API_KEY": "f", "NODE_OPTIONS": "--inspect"}
	environ := []string{"HOME=/home/x", "USER=x", "LANG=C", "PATH=/usr/bin:/bin"}
	for k, v := range secrets {
		environ = append(environ, k+"="+v)
	}
	h := &LSPHost{Environ: func() []string { return environ }, FixedDirs: []string{},
		GoEnv: func(context.Context) map[string]string {
			return map[string]string{"GOPATH": "/gp", "GOFLAGS": "-mod=mod"}
		},
		NPMPrefix: func(context.Context) string { return "" }}
	p, err := h.Start(context.Background(), goplsSpec(), fakeServer(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	var env []string
	_ = json.Unmarshal(request(t, p, 1, "env", nil), &env)
	joined := "\n" + strings.Join(env, "\n")
	for k := range secrets {
		if strings.Contains(joined, "\n"+k+"=") {
			t.Fatalf("%s leaked into the server environment", k)
		}
	}
	for _, want := range []string{"HOME=/home/x", "GOPATH=/gp", "GOFLAGS=-mod=mod"} {
		if !strings.Contains(joined, "\n"+want) {
			t.Fatalf("missing %s in %v", want, env)
		}
	}
}

func TestStartRejectsServersOutsideTheAllowlist(t *testing.T) {
	h := &LSPHost{}
	if _, err := h.Start(context.Background(), model.LSPServerSpec{ID: "rm", Binary: "rm"}, "/bin/rm", t.TempDir()); err == nil {
		t.Fatal("non-allowlisted server started")
	}
}

func TestKillTakesDownTheWholeProcessGroup(t *testing.T) {
	h := &LSPHost{FixedDirs: []string{}, GoEnv: func(context.Context) map[string]string { return nil }, NPMPrefix: func(context.Context) string { return "" }}
	p, err := h.Start(context.Background(), goplsSpec(), fakeServer(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var child int
	_ = json.Unmarshal(request(t, p, 1, "spawnchild", nil), &child)
	if child == 0 || syscall.Kill(child, 0) != nil {
		t.Fatalf("child not running: %d", child)
	}
	_ = p.Kill()
	_ = p.Wait()
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(child, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(child, 0) == nil {
		syscall.Kill(child, syscall.SIGKILL)
		t.Fatal("grandchild survived the group kill")
	}
}

func TestStderrTailKeepsLast4KB(t *testing.T) {
	h := &LSPHost{FixedDirs: []string{}, GoEnv: func(context.Context) map[string]string { return nil }, NPMPrefix: func(context.Context) string { return "" }}
	p, err := h.Start(context.Background(), goplsSpec(), fakeServer(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	request(t, p, 1, "stderr", nil)
	deadline := time.Now().Add(2 * time.Second)
	for !strings.HasSuffix(p.StderrTail(), "END") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if tail := p.StderrTail(); len(tail) != lspTailBytes || !strings.HasSuffix(tail, "END") {
		t.Fatalf("tail len %d", len(tail))
	}
}

func TestDiscoverFindsServersOnTheAugmentedPath(t *testing.T) {
	gobin := t.TempDir()
	brew := t.TempDir()
	login := t.TempDir()
	write := func(dir, name, out string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\necho '"+out+"'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(gobin, "gopls", "golang.org/x/tools/gopls v0.21.1")
	write(brew, "typescript-language-server", "6.1.0")
	write(login, "node", "v24")
	shell := filepath.Join(t.TempDir(), "fakeshell")
	script := "#!/bin/sh\necho 'rc noise'\nprintf '__LEGOS_PATH__" + login + "__LEGOS_PATH__'\n"
	if err := os.WriteFile(shell, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	h := &LSPHost{Environ: func() []string { return []string{"PATH=/nonexistent"} }, Shell: shell, FixedDirs: []string{brew},
		GoEnv: func(context.Context) map[string]string {
			return map[string]string{"GOPATH": filepath.Dir(gobin) + "/x:" + filepath.Dir(gobin)}
		},
		NPMPrefix: func(context.Context) string { return "" }}
	// GOPATH/bin must be where gopls lives: build that layout.
	gopath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(gopath, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(gopath, "bin"), "gopls", "golang.org/x/tools/gopls v0.21.1")
	h.GoEnv = func(context.Context) map[string]string { return map[string]string{"GOPATH": gopath} }

	got := h.Discover(context.Background(), LSPAllowlist, nil)
	if got[0].ID != "gopls" || got[0].State != model.LSPFound || got[0].Version != "0.21.1" || got[0].Path != filepath.Join(gopath, "bin", "gopls") {
		t.Fatalf("gopls: %+v", got[0])
	}
	if got[1].State != model.LSPFound || got[1].Version != "6.1.0" {
		t.Fatalf("tsls (Homebrew dir and login-shell node): %+v", got[1])
	}
}

func TestDiscoverNeedsNodeAndCustomPath(t *testing.T) {
	brew := t.TempDir()
	if err := os.WriteFile(filepath.Join(brew, "typescript-language-server"), []byte("#!/bin/sh\necho 6.1.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := &LSPHost{Environ: func() []string { return []string{"PATH=/nonexistent"} }, Shell: "/nonexistent-shell", FixedDirs: []string{brew},
		GoEnv: func(context.Context) map[string]string { return nil }, NPMPrefix: func(context.Context) string { return "" }}
	got := h.Discover(context.Background(), LSPAllowlist, nil)
	if got[1].State != model.LSPNotFound || got[1].Error != "needs Node.js" {
		t.Fatalf("no node: %+v", got[1])
	}
	if got[0].State != model.LSPNotFound || got[0].Install == "" {
		t.Fatalf("gopls absent: %+v", got[0])
	}

	custom := filepath.Join(t.TempDir(), "mygopls")
	if err := os.WriteFile(custom, []byte("#!/bin/sh\necho 'golang.org/x/tools/gopls v0.99.0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got = h.Discover(context.Background(), LSPAllowlist[:1], map[string]string{"gopls": custom})
	if got[0].State != model.LSPFound || got[0].Path != custom || got[0].Version != "0.99.0" {
		t.Fatalf("custom: %+v", got[0])
	}
	got = h.Discover(context.Background(), LSPAllowlist[:1], map[string]string{"gopls": "/does/not/exist"})
	if got[0].State != model.LSPNotFound || got[0].Error == "" {
		t.Fatalf("bad custom: %+v", got[0])
	}
}

func TestLoginShellTimeoutIsBounded(t *testing.T) {
	shell := filepath.Join(t.TempDir(), "slowshell")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := &LSPHost{Environ: func() []string { return []string{"PATH=/usr/bin"} }, Shell: shell, ShellTimeout: 200 * time.Millisecond,
		GoEnv: func(context.Context) map[string]string { return nil }, NPMPrefix: func(context.Context) string { return "" }}
	start := time.Now()
	h.Discover(context.Background(), LSPAllowlist, nil)
	if time.Since(start) > 5*time.Second {
		t.Fatalf("login shell was not cut off: %v", time.Since(start))
	}
}

func TestGoplsOutdatedComparesGoLine(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n\ngo 1.26.3\n"), 0o644)
	h := &LSPHost{builtWith: map[string]string{"/b/gopls": "go1.24.0"}}
	if out, built, need := h.GoplsOutdated(context.Background(), "/b/gopls", root); !out || built != "go1.24.0" || need != "go1.26.3" {
		t.Fatalf("older: %v %s %s", out, built, need)
	}
	h.builtWith["/b/gopls"] = "go1.26.3"
	if out, _, _ := h.GoplsOutdated(context.Background(), "/b/gopls", root); out {
		t.Fatal("equal must not be outdated")
	}
	h.builtWith["/b/gopls"] = "go1.27.1"
	if out, _, _ := h.GoplsOutdated(context.Background(), "/b/gopls", root); out {
		t.Fatal("newer must not be outdated")
	}
	if out, _, need := h.GoplsOutdated(context.Background(), "/b/gopls", t.TempDir()); out || need != "" {
		t.Fatal("no go.mod: never outdated")
	}
}
