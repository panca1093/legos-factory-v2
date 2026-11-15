package usecase

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"legos-factory/backend/internal/model"
)

const (
	chatGitTimeout = 3 * time.Second
	cacheWarmFor   = 5 * time.Minute // TODO: uncertain — TTL may be 1h on subscription logins, verify before prod
)

var shaPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// turnInfo is what one chat turn records beyond the harness result; steps arrive on the harness goroutine, so calls is guarded.
type turnInfo struct {
	newSession  bool
	sha         string
	project     string
	steps       int
	promptBytes int
	factsBytes  int
	mcpBytes    int

	mu    sync.Mutex
	calls map[string]int
}

func (t *turnInfo) countCalls(tools []model.AgentTool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.steps++
	for _, tool := range tools {
		rest, ok := strings.CutPrefix(tool.Name, "mcp__")
		if !ok {
			continue
		}
		server, name, ok := strings.Cut(rest, "__")
		if !ok || server == "" || name == "" {
			continue
		}
		if t.calls == nil {
			t.calls = map[string]int{}
		}
		t.calls[server+"/"+name]++
	}
}

func (t *turnInfo) usage(modelRef string, res model.AgentResult, atts []model.ChatAttachment) *model.ChatUsage {
	t.mu.Lock()
	defer t.mu.Unlock()
	attach := 0
	for _, a := range atts {
		if a.Kind == "file" || a.Kind == "image" {
			attach += a.Size
		}
	}
	u := &model.ChatUsage{
		Model: modelRef, TokensIn: res.TokensIn, TokensOut: res.TokensOut, CostMicros: res.CostMicros,
		CacheRead: res.CacheRead, CacheWrite: res.CacheWrite, RepoSHA: t.sha, RepoProject: t.project, Steps: t.steps, ContextUsed: res.ContextUsed, ContextWindow: res.ContextSize, NewSession: t.newSession,
		PromptBytes: t.promptBytes, FactsBytes: t.factsBytes, MCPToolBytes: t.mcpBytes, AttachBytes: attach,
	}
	if len(t.calls) > 0 {
		u.MCPCalls = make(map[string]int, len(t.calls))
		for k, v := range t.calls {
			u.MCPCalls[k] = v
		}
	}
	return u
}

// ChatSession is the data behind the chat's session rail.
type ChatSession struct {
	Project *SessionProject `json:"project"`
	Context SessionContext  `json:"context"`
	Totals  SessionTotals   `json:"totals"`
	MCP     []SessionMCP    `json:"mcp"`
}

type SessionProject struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Local     *SessionLocal    `json:"local"`
	Worktrees []SessionTree    `json:"worktrees"`
	Checkout  *SessionCheckout `json:"checkout"`
	Running   []SessionRunning `json:"running"`
}

type SessionLocal struct {
	Path   string  `json:"path"`
	Branch *string `json:"branch"`
	Head   *string `json:"head"`
	Dirty  *int    `json:"dirty"`
}

type SessionTree struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Head   string `json:"head"`
	Main   bool   `json:"main"`
}

type SessionCheckout struct {
	SHA         string `json:"sha"`
	BehindLocal *int   `json:"behind_local"`
}

type SessionRunning struct {
	WorkOrderID string `json:"work_order_id"`
	Title       string `json:"title"`
	State       string `json:"state"`
	Role        string `json:"role"`
	TaskID      string `json:"task_id"`
	Since       string `json:"since"`
	Workspace   string `json:"workspace"`
}

type SessionContext struct {
	Model  string        `json:"model"`
	Window int           `json:"window"`
	Used   int64         `json:"used"`
	Parts  []SessionPart `json:"parts"`
	Cache  *SessionCache `json:"cache"`
}

type SessionPart struct {
	Key    string `json:"key"`
	Tokens int64  `json:"tokens"`
}

type SessionCache struct {
	Read       int64  `json:"read"`
	Write      int64  `json:"write"`
	Uncached   int64  `json:"uncached"`
	HitPct     int    `json:"hit_pct"`
	LastTurnAt string `json:"last_turn_at"`
	WarmUntil  string `json:"warm_until"`
}

type SessionTotals struct {
	Turns      int   `json:"turns"`
	TokensIn   int64 `json:"tokens_in"`
	TokensOut  int64 `json:"tokens_out"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	CostMicros int64 `json:"cost_micros"`
}

type SessionMCP struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Transport    string         `json:"transport"`
	Auth         string         `json:"auth"`
	Status       string         `json:"status"`
	Offered      bool           `json:"offered"`
	ToolsEnabled int            `json:"tools_enabled"`
	ToolsTotal   int            `json:"tools_total"`
	Asks         bool           `json:"asks"`
	Calls        map[string]int `json:"calls"`
}

// contextWindow is the model's context size in tokens, 0 when unknown.
// TODO: uncertain — ACP model ids and real windows not verified per model, verify before prod
func contextWindow(modelRef string) int {
	switch {
	case strings.Contains(modelRef, "[1m]"):
		return 1_000_000
	case strings.HasPrefix(modelRef, "claude/"):
		return 200_000
	}
	return 0
}

// Session reads the rail's data for the user's chat. It never takes the chat lock, so a running turn does not delay it.
func (c *Chat) Session(ctx context.Context, u *model.User, id string) (*ChatSession, error) {
	ch, err := c.own(ctx, u, id)
	if err != nil {
		return nil, err
	}
	msgs, err := c.Store.Messages(ctx, id)
	if err != nil {
		return nil, err
	}
	out := &ChatSession{MCP: []SessionMCP{}}
	out.Context, out.Totals = sessionContext(c.resolve(ctx, ch), msgs)
	calls := map[string]map[string]int{}
	for _, m := range msgs {
		if m.Role != "assistant" || m.Usage == nil {
			continue
		}
		for k, n := range m.Usage.MCPCalls {
			if server, tool, ok := strings.Cut(k, "/"); ok {
				if calls[server] == nil {
					calls[server] = map[string]int{}
				}
				calls[server][tool] += n
			}
		}
	}
	out.MCP = c.sessionMCP(ctx, u.Role.IsAdmin(), calls)
	if ch.ProjectID != "" && c.Projects != nil {
		if p, err := c.Projects.Get(ctx, u, ch.ProjectID); err == nil && p != nil {
			out.Project = c.sessionProject(ctx, p, msgs)
		}
	}
	return out, nil
}

func sessionContext(modelRef string, msgs []model.ChatMessage) (SessionContext, SessionTotals) {
	sc := SessionContext{Model: modelRef, Window: contextWindow(modelRef), Parts: []SessionPart{}}
	var tot SessionTotals
	last, start, clean := -1, -1, -1
	for i, m := range msgs {
		if m.Role != "assistant" || m.Usage == nil {
			continue
		}
		tot.Turns++
		tot.TokensIn += m.Usage.TokensIn
		tot.TokensOut += m.Usage.TokensOut
		tot.CacheRead += m.Usage.CacheRead
		tot.CacheWrite += m.Usage.CacheWrite
		tot.CostMicros += m.Usage.CostMicros
		if m.Error != "" {
			continue
		}
		last = i
		if m.Usage.NewSession {
			start = i
			if m.Usage.Steps == 0 {
				clean = i
			}
		}
	}
	if last < 0 {
		return sc, tot
	}
	lu := msgs[last].Usage
	sc.Used = lu.TokensIn + lu.TokensOut
	if lu.ContextUsed > 0 {
		sc.Used = lu.ContextUsed
	}
	if lu.ContextWindow > 0 {
		sc.Window = int(lu.ContextWindow)
	}
	if lu.TokensIn > 0 {
		sc.Cache = &SessionCache{
			Read: lu.CacheRead, Write: lu.CacheWrite, Uncached: max(0, lu.TokensIn-lu.CacheRead-lu.CacheWrite),
			HitPct:     int((lu.CacheRead*100 + lu.TokensIn/2) / lu.TokensIn),
			LastTurnAt: msgs[last].CreatedAt.Format(time.RFC3339),
			WarmUntil:  msgs[last].CreatedAt.Add(cacheWarmFor).Format(time.RFC3339),
		}
	}
	if start < 0 {
		return sc, tot
	}
	su := msgs[start].Usage
	mcp, facts := int64(su.MCPToolBytes/4), int64(su.FactsBytes/4)
	var files int64
	for _, m := range msgs[start:] {
		if m.Role == "assistant" && m.Usage != nil && m.Error == "" {
			files += int64(m.Usage.AttachBytes / 4)
		}
	}
	var agent int64
	// a session start that used tools has tool results in its context, so the agent's own share comes from the latest clean start in this chat
	if clean >= 0 {
		cu := msgs[clean].Usage
		base := cu.TokensIn
		if cu.ContextUsed > 0 {
			base = cu.ContextUsed
		}
		agent = max(0, base-int64(cu.PromptBytes/4)-int64(cu.MCPToolBytes/4))
	}
	sc.Parts = []SessionPart{
		{"agent", agent}, {"mcp", mcp}, {"facts", facts}, {"files", files},
		{"messages", max(0, sc.Used-agent-mcp-facts-files)},
	}
	return sc, tot
}

func (c *Chat) sessionMCP(ctx context.Context, admin bool, calls map[string]map[string]int) []SessionMCP {
	out := []SessionMCP{}
	if c.MCPServers == nil {
		return out
	}
	recs, err := c.MCPServers.List(ctx)
	if err != nil {
		return out
	}
	for _, r := range recs {
		s := SessionMCP{ID: r.ID, Name: r.Name, Transport: r.Transport, Auth: r.Auth, Status: r.Status, ToolsTotal: len(r.Tools), Calls: calls[r.Name]}
		if s.Calls == nil {
			s.Calls = map[string]int{}
		}
		writes := false
		for _, t := range r.Tools {
			if t.Enabled && !t.Unusable {
				s.ToolsEnabled++
				writes = writes || !t.ReadOnly
			}
		}
		s.Offered = r.Status == model.MCPActive && slices.Contains(r.Roles, string(model.AgentChat)) && s.ToolsEnabled > 0
		s.Asks = s.Offered && writes
		if !admin && !s.Offered {
			continue
		}
		out = append(out, s)
	}
	return out
}

func (c *Chat) sessionProject(ctx context.Context, p *model.Project, msgs []model.ChatMessage) *SessionProject {
	sp := &SessionProject{ID: p.ID, Name: p.Name, Worktrees: []SessionTree{}, Running: []SessionRunning{}}
	gctx, cancel := context.WithTimeout(ctx, chatGitTimeout)
	defer cancel()
	dir := localDir(p.RepoURL)
	if dir != "" {
		sp.Local, sp.Worktrees = gitLocal(gctx, dir)
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if m := msgs[i]; m.Role == "assistant" && m.Usage != nil && m.Usage.RepoSHA != "" && m.Usage.RepoProject == p.ID {
			sp.Checkout = &SessionCheckout{SHA: m.Usage.RepoSHA}
			if dir != "" && shaPattern.MatchString(m.Usage.RepoSHA) {
				if n, err := strconv.Atoi(gitOut(gctx, dir, "rev-list", "--count", m.Usage.RepoSHA+"..HEAD")); err == nil {
					sp.Checkout.BehindLocal = &n
				}
			}
			break
		}
	}
	if c.Runs != nil {
		runs, _ := c.Runs.RunningByProject(ctx, p.ID)
		for _, r := range runs {
			if r.WOState.Terminal() {
				continue
			}
			ws := ""
			if c.DataDir != "" {
				ws = filepath.Join(c.DataDir, "workspaces", r.WOID)
			}
			sp.Running = append(sp.Running, SessionRunning{
				WorkOrderID: r.WOID, Title: r.WOTitle, State: string(r.WOState), Role: r.Kind, TaskID: r.TaskID,
				Since: r.StartedAt.Format(time.RFC3339), Workspace: ws,
			})
		}
	}
	return sp
}

// localDir is the project's folder when its repo_url is an existing local directory, else "".
func localDir(repoURL string) string {
	if repoURL == "" || strings.Contains(repoURL, "://") || !filepath.IsAbs(repoURL) {
		return ""
	}
	if fi, err := os.Stat(repoURL); err != nil || !fi.IsDir() {
		return ""
	}
	return repoURL
}

// gitOut runs one read-only git command in dir without a shell; the repo's own config must not run commands. A failure gives "".
func gitOut(ctx context.Context, dir string, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-C", dir}, args...)...)
	cmd.WaitDelay = time.Second
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	b, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func gitLocal(ctx context.Context, dir string) (*SessionLocal, []SessionTree) {
	l := &SessionLocal{Path: dir}
	if s := gitOut(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD"); s != "" {
		l.Branch = &s
	}
	if s := gitOut(ctx, dir, "rev-parse", "HEAD"); s != "" {
		l.Head = &s
	}
	if l.Head != nil {
		n := 0
		if s := gitOut(ctx, dir, "status", "--porcelain"); s != "" {
			n = len(strings.Split(s, "\n"))
		}
		l.Dirty = &n
	}
	return l, parseWorktrees(gitOut(ctx, dir, "worktree", "list", "--porcelain"))
}

func parseWorktrees(porcelain string) []SessionTree {
	out := []SessionTree{}
	var cur *SessionTree
	for _, line := range strings.Split(porcelain, "\n") {
		key, val, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch key {
		case "worktree":
			out = append(out, SessionTree{Path: val, Main: len(out) == 0})
			cur = &out[len(out)-1]
		case "HEAD":
			if cur != nil {
				cur.Head = val
			}
		case "branch":
			if cur != nil {
				cur.Branch = strings.TrimPrefix(val, "refs/heads/")
			}
		}
	}
	return out
}
