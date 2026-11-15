package usecase

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

func asst(at time.Time, u model.ChatUsage) model.ChatMessage {
	return model.ChatMessage{Role: "assistant", Usage: &u, CreatedAt: at}
}

func partsOf(sc SessionContext) map[string]int64 {
	m := map[string]int64{}
	for _, p := range sc.Parts {
		m[p.Key] = p.Tokens
	}
	return m
}

func TestSessionPartsAtSessionStartAndAfterContinue(t *testing.T) {
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	start := asst(at, model.ChatUsage{TokensIn: 10_000, TokensOut: 500, NewSession: true, PromptBytes: 8_000, FactsBytes: 4_000, MCPToolBytes: 2_000, AttachBytes: 400, CacheRead: 6_000, CacheWrite: 3_000})
	sc, tot := sessionContext("claude/sonnet", []model.ChatMessage{{Role: "user"}, start})
	got := partsOf(sc)
	// agent = 10000 - 8000/4 - 2000/4 = 7500; messages = 10500 - 7500 - 500 - 1000 - 100
	want := map[string]int64{"agent": 7500, "mcp": 500, "facts": 1000, "files": 100, "messages": 1400}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: got %d want %d (%+v)", k, got[k], v, sc.Parts)
		}
	}
	if sc.Used != 10_500 || sc.Window != 200_000 || tot.Turns != 1 {
		t.Fatalf("context %+v totals %+v", sc, tot)
	}
	if c := sc.Cache; c == nil || c.HitPct != 60 || c.Uncached != 1_000 || c.WarmUntil != at.Add(5*time.Minute).Format(time.RFC3339) {
		t.Fatalf("cache %+v", c)
	}

	cont := asst(at.Add(time.Minute), model.ChatUsage{TokensIn: 12_000, TokensOut: 300, AttachBytes: 800})
	sc, tot = sessionContext("claude/sonnet", []model.ChatMessage{{Role: "user"}, start, {Role: "user"}, cont})
	got = partsOf(sc)
	if sc.Used != 12_300 || got["agent"] != 7500 || got["mcp"] != 500 || got["facts"] != 1000 || got["files"] != 300 || got["messages"] != 3000 {
		t.Fatalf("after continue: used %d %+v", sc.Used, sc.Parts)
	}
	if tot.Turns != 2 || tot.TokensIn != 22_000 || tot.CacheRead != 6_000 {
		t.Fatalf("totals %+v", tot)
	}
}

func TestSessionPartsEmptyForAnOldChatAndFailedTurnsDoNotSetUsed(t *testing.T) {
	at := time.Now()
	old := asst(at, model.ChatUsage{TokensIn: 900, TokensOut: 100})
	sc, _ := sessionContext("anthropic/opus", []model.ChatMessage{old})
	if sc.Used != 1000 || len(sc.Parts) != 0 || sc.Parts == nil || sc.Window != 0 {
		t.Fatalf("old chat: %+v", sc)
	}

	failed := asst(at, model.ChatUsage{TokensIn: 50_000, TokensOut: 1})
	failed.Error = "agent exited"
	sc, tot := sessionContext("claude/sonnet", []model.ChatMessage{old, failed})
	if sc.Used != 1000 || tot.Turns != 2 {
		t.Fatalf("failed turn: used %d totals %+v", sc.Used, tot)
	}
	if sc, _ = sessionContext("claude/sonnet[1m]", nil); sc.Window != 1_000_000 || sc.Cache != nil || sc.Parts == nil {
		t.Fatalf("empty chat: %+v", sc)
	}
}

func TestChatRecordsTurnUsageAndCountsMCPCalls(t *testing.T) {
	ctx := context.Background()
	steps := []model.AgentStep{
		{N: 1, Tools: []model.AgentTool{{Name: "mcp__workbench_amartha__whoami"}, {Name: "Read File"}, {Name: "mcp__workbench_amartha__whoami"}}},
		{N: 2, Tools: []model.AgentTool{{Name: "mcp__docs__search"}}},
	}
	h := &chatKeeper{chatPlain: &chatPlain{reply: "ok", steps: steps}}
	r := repoRigWith(t, h)
	r.chat.MCP = &fakeSelector{sel: model.MCPSelection{Servers: testSelection.Servers, Allow: testSelection.Allow, Bytes: 4_321}}
	ch, _ := r.chat.Create(ctx, r.ana, "", "p1", "")
	files := []ChatFile{{Name: "n.txt", Content: "abcd"}}
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q", Files: files}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "again"}); err != nil {
		t.Fatal(err)
	}
	msgs, _ := r.chat.Messages(ctx, r.ana, ch.ID)
	u1, u2 := msgs[1].Usage, msgs[3].Usage
	if !u1.NewSession || u1.RepoSHA == "" || u1.MCPToolBytes != 4_321 || u1.FactsBytes == 0 || u1.AttachBytes != 4 || u1.PromptBytes != len(h.starts[0].Prompt) {
		t.Fatalf("first turn: %+v", u1)
	}
	if u1.MCPCalls["workbench_amartha/whoami"] != 2 || u1.MCPCalls["docs/search"] != 1 || len(u1.MCPCalls) != 2 {
		t.Fatalf("calls: %+v", u1.MCPCalls)
	}
	if u2.NewSession || u2.FactsBytes != 0 || u2.PromptBytes != len("again") || u2.RepoSHA != u1.RepoSHA {
		t.Fatalf("continue turn: %+v", u2)
	}
}

func TestChatFailedTurnStoresWhatIsKnown(t *testing.T) {
	ctx := context.Background()
	h := &chatPlain{reply: "partial", err: errors.New("agent exited"), errRes: model.AgentResult{TokensIn: 70, TokensOut: 3, CacheRead: 40},
		steps: []model.AgentStep{{N: 1, Tools: []model.AgentTool{{Name: "mcp__docs__search"}}}}}
	r := newChatRig(t, h)
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q"}); err != nil {
		t.Fatal(err)
	}
	msgs, _ := r.chat.Messages(ctx, r.ana, ch.ID)
	u := msgs[1].Usage
	if msgs[1].Error == "" || u == nil || !u.NewSession || u.TokensIn != 70 || u.CacheRead != 40 || u.MCPCalls["docs/search"] != 1 {
		t.Fatalf("failed turn: %+v %+v", msgs[1], u)
	}
	ses, err := r.chat.Session(ctx, r.ana, ch.ID)
	if err != nil || ses.Context.Used != 0 || ses.Totals.Turns != 1 || len(ses.Context.Parts) != 0 {
		t.Fatalf("session after a failed turn: %v %+v", err, ses)
	}
}

func TestChatSessionMCPAndOwnership(t *testing.T) {
	ctx := context.Background()
	r := newChatRig(t, &chatPlain{reply: "ok"})
	r.chat.MCPServers = fakeMCPList{recs: []model.MCPServerRec{
		{ID: "m1", Name: "docs", Transport: model.MCPHTTP, Auth: model.MCPAuthOAuth, Status: model.MCPActive, Roles: []string{"chat"},
			Tools: []model.MCPTool{{Name: "search", Enabled: true, ReadOnly: true}, {Name: "write", Enabled: true}, {Name: "off"}, {Name: "bad", Enabled: true, Unusable: true}}},
		{ID: "m2", Name: "other", Transport: model.MCPStdio, Status: model.MCPActive, Roles: []string{"builder"}, Tools: []model.MCPTool{{Name: "x", Enabled: true}}},
		{ID: "m3", Name: "ro", Transport: model.MCPHTTP, Status: model.MCPActive, Roles: []string{"chat"}, Tools: []model.MCPTool{{Name: "get", Enabled: true, ReadOnly: true}}},
	}}
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	ses, err := r.chat.Session(ctx, r.ana, ch.ID)
	if err != nil || ses.Project != nil || len(ses.MCP) != 3 {
		t.Fatalf("session: %v %+v", err, ses)
	}
	d, o, ro := ses.MCP[0], ses.MCP[1], ses.MCP[2]
	if !d.Offered || d.ToolsEnabled != 2 || d.ToolsTotal != 4 || !d.Asks || d.Calls == nil {
		t.Fatalf("docs: %+v", d)
	}
	if o.Offered || o.Asks || !ro.Offered || ro.Asks {
		t.Fatalf("other %+v ro %+v", o, ro)
	}
	if _, err := r.chat.Session(ctx, r.bob, ch.ID); !errors.Is(err, model.ErrChatNotFound) {
		t.Fatalf("another user's chat: %v", err)
	}
}

func TestChatSessionProjectLostShowsNone(t *testing.T) {
	ctx := context.Background()
	r := newChatRig(t, &chatPlain{reply: "ok"})
	ch, _ := r.chat.Create(ctx, r.ana, "", "p1", "")
	ses, err := r.chat.Session(ctx, r.ana, ch.ID)
	if err != nil || ses.Project == nil || ses.Project.ID != "p1" || ses.Project.Local != nil || ses.Project.Worktrees == nil || ses.Project.Running == nil {
		t.Fatalf("member: %v %+v", err, ses.Project)
	}
	if err := r.members.Remove(ctx, "p1", "ana"); err != nil {
		t.Fatal(err)
	}
	if ses, err = r.chat.Session(ctx, r.ana, ch.ID); err != nil || ses.Project != nil {
		t.Fatalf("lost access: %v %+v", err, ses.Project)
	}
}

type fakeMCPList struct{ recs []model.MCPServerRec }

func (f fakeMCPList) List(context.Context) ([]model.MCPServerRec, error) { return f.recs, nil }

func repoRigWith(t *testing.T, h Harness) *chatRig {
	t.Helper()
	r, _, _, _ := repoRig(t, h)
	return r
}

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "one")
	return dir
}

func TestGitLocalReadsBranchHeadDirtyAndWorktrees(t *testing.T) {
	dir := gitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	if out, err := exec.Command("git", "-C", dir, "worktree", "add", "-q", "-b", "feat/x", wt).CombinedOutput(); err != nil {
		t.Fatalf("worktree: %v %s", err, out)
	}
	ctx := context.Background()
	l, trees := gitLocal(ctx, dir)
	if l.Branch == nil || *l.Branch != "main" || l.Head == nil || len(*l.Head) != 40 || l.Dirty == nil || *l.Dirty != 1 {
		t.Fatalf("local: %+v", l)
	}
	if len(trees) != 2 || !trees[0].Main || trees[0].Branch != "main" || trees[1].Main || trees[1].Branch != "feat/x" || trees[1].Head == "" {
		t.Fatalf("worktrees: %+v", trees)
	}
	if n := gitOut(ctx, dir, "rev-list", "--count", *l.Head+"..HEAD"); n != "0" {
		t.Fatalf("behind: %q", n)
	}

	l, trees = gitLocal(ctx, t.TempDir())
	if l.Branch != nil || l.Head != nil || l.Dirty != nil || len(trees) != 0 {
		t.Fatalf("not a repo: %+v %+v", l, trees)
	}
}

func TestGitLocalIgnoresTheReposOwnCommandConfig(t *testing.T) {
	dir := gitRepo(t)
	marker := filepath.Join(t.TempDir(), "ran")
	hook := filepath.Join(t.TempDir(), "fsmonitor.sh")
	script := "#!/bin/sh\ntouch " + marker + "\nprintf '\\0'\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "config", "core.fsmonitor", hook).CombinedOutput(); err != nil {
		t.Fatalf("config: %v %s", err, out)
	}
	_ = exec.Command("git", "-C", dir, "status", "--porcelain").Run()
	if _, err := os.Stat(marker); err != nil {
		t.Skip("this git does not run core.fsmonitor commands; the check would prove nothing")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	gitLocal(context.Background(), dir)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the repo's core.fsmonitor command ran")
	}
}

func TestLocalDirOnlyForExistingAbsoluteFolders(t *testing.T) {
	dir := t.TempDir()
	for in, want := range map[string]string{
		dir: dir, "": "", "https://github.com/a/b": "", "git@github.com:a/b.git": "", "relative/dir": "", filepath.Join(dir, "missing"): "",
	} {
		if got := localDir(in); got != want {
			t.Fatalf("localDir(%q) = %q, want %q", in, got, want)
		}
	}
	f := filepath.Join(dir, "file")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if localDir(f) != "" {
		t.Fatal("a file is not a folder")
	}
}

func TestContextWindowTable(t *testing.T) {
	for in, want := range map[string]int{"claude/sonnet": 200_000, "claude/opus[1m]": 1_000_000, "anthropic/opus": 0, "": 0} {
		if got := contextWindow(in); got != want {
			t.Fatalf("contextWindow(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestCountCallsIgnoresNonMCPTools(t *testing.T) {
	var ti turnInfo
	ti.countCalls([]model.AgentTool{{Name: "Read File"}, {Name: "mcp__x"}, {Name: "mcp____y"}, {Name: "mcp__a__b__c"}})
	if len(ti.calls) != 1 || ti.calls["a/b__c"] != 1 || strings.Contains(ti.usage("m", model.AgentResult{}, nil).Model, "x") {
		t.Fatalf("calls: %+v", ti.calls)
	}
}
