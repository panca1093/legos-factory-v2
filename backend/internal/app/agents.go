package app

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"legos-factory/backend/internal/agent"
	"legos-factory/backend/internal/integration"
	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/usecase"
)

// harnessSet holds one ACP harness per agent, built the first time a run needs it.
type harnessSet struct {
	agents *usecase.Agents
	probe  string
	mu     sync.Mutex
	m      map[string]*agent.ACPHarness
}

func (hs *harnessSet) get(id string) (*agent.ACPHarness, error) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	if h, ok := hs.m[id]; ok {
		return h, nil
	}
	cmd, err := hs.agents.Command(id)
	if err != nil {
		return nil, err
	}
	h := agent.NewACPHarness(cmd, filepath.Join(hs.probe, id))
	if hs.m == nil {
		hs.m = map[string]*agent.ACPHarness{}
	}
	hs.m[id] = h
	return h, nil
}

// unavailable is the harness of a role whose agent cannot run: every run fails at once, permanently, with the reason.
type unavailable struct {
	name string
	err  error
}

func (u unavailable) Start(context.Context, model.AgentRequest) (model.AgentSession, error) {
	return nil, u.err
}
func (u unavailable) Cancel(context.Context, string) error { return nil }
func (u unavailable) Result(context.Context, string) (model.AgentResult, error) {
	return model.AgentResult{}, u.err
}
func (u unavailable) Name() string { return u.name }

// catalogs caches each agent's model list per account, so a run never waits on a probe it has already done.
type catalogs struct {
	hs *harnessSet
	// probe lists an agent's models under an account env; nil asks the agent itself.
	probe func(ctx context.Context, id string, env, args []string) ([]agent.Model, error)
	mu    sync.Mutex
	m     map[string]*catalogEntry
	locks map[string]*sync.Mutex
}

type catalogEntry struct {
	at     time.Time
	models []model.ModelInfo
}

const catalogTTL = 10 * time.Minute

func (c *catalogs) key(id, user, sig string) string { return id + "\x00" + user + "\x00" + sig }

// peek returns the cached list without fetching.
func (c *catalogs) peek(id, user string) []model.ModelInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.m[c.key(id, user, "")]; e != nil {
		return e.models
	}
	return nil
}

// list returns the agent's models under the account serving role for the user (the active one when role is empty), fetching
// when stale. A failed fetch keeps the stale list. The key holds the account and its model map, so switching or editing it
// never serves the old list.
func (c *catalogs) list(ctx context.Context, agents *usecase.Agents, id, user string, role model.AgentRole, fresh bool) ([]model.ModelInfo, error) {
	api, err := agents.RoleAPI(ctx, id, user, role)
	if err != nil {
		return nil, err
	}
	k := c.key(id, user, api.Sig)
	c.mu.Lock()
	if c.locks == nil {
		c.locks = map[string]*sync.Mutex{}
		c.m = map[string]*catalogEntry{}
	}
	lk := c.locks[k]
	if lk == nil {
		lk = &sync.Mutex{}
		c.locks[k] = lk
	}
	c.mu.Unlock()
	lk.Lock()
	defer lk.Unlock()
	c.mu.Lock()
	e := c.m[k]
	c.mu.Unlock()
	if e != nil && !fresh && time.Since(e.at) < catalogTTL {
		return e.models, nil
	}
	probe := c.probe
	if probe == nil {
		h, err := c.hs.get(id)
		if err != nil {
			return nil, err
		}
		probe = func(ctx context.Context, _ string, env, args []string) ([]agent.Model, error) {
			return h.ModelsFor(ctx, env, args)
		}
	}
	env, args, err := agents.RunEnv(ctx, id, user, role)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ms, err := probe(pctx, id, env, args)
	if err != nil {
		if e != nil {
			return e.models, nil
		}
		return nil, err
	}
	out := make([]model.ModelInfo, 0, len(ms))
	for _, m := range ms {
		out = append(out, model.ModelInfo{ProviderID: m.ProviderID, ModelID: m.ModelID, Name: m.Name, Harness: id})
	}
	if api.Models != nil {
		out = usecase.FoldModels(*api.Models, out)
	}
	c.mu.Lock()
	c.m[k] = &catalogEntry{at: time.Now(), models: out}
	c.mu.Unlock()
	return out, nil
}

func refs(ms []model.ModelInfo) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ProviderID + "/" + m.ModelID
	}
	return out
}

// providerAgent names the known agent an ACP command runs, or "".
func providerAgent(command []string) string {
	for _, a := range command {
		base := filepath.Base(a)
		for _, s := range usecase.AgentSpecs {
			if strings.HasPrefix(base, s.ID) {
				return s.ID
			}
		}
	}
	return ""
}

// jev follows the workspace Jev toggle and key: settings changes rebuild the judge, runs only read it.
type jev struct {
	mu    sync.RWMutex
	key   string
	model string
	j     usecase.Judge
}

func (v *jev) get() usecase.Judge {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.j
}

func (v *jev) set(key string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if key == v.key {
		return
	}
	v.key = key
	if key == "" {
		v.j = nil
		return
	}
	v.j = jevJudge{integration.NewTypeSafe(key, v.model, "")}
}
