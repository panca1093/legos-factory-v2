package usecase

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

type recStore struct {
	mu   sync.Mutex
	recs []model.UsageRecord
}

func (s *recStore) Insert(_ context.Context, u model.UsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, u)
	return nil
}
func (*recStore) Totals(context.Context) (model.UsageTotals, error) { return model.UsageTotals{}, nil }
func (*recStore) SpentMicros(context.Context) int64                  { return 0 }
func (*recStore) Since(context.Context, string, time.Time) ([]model.UsageRecord, error) {
	return nil, nil
}
func (*recStore) AccountLabels(context.Context, string) (map[string]model.UsageAccountLabel, error) {
	return nil, nil
}

type costHarness struct{ steppingHarness }

func (*costHarness) Result(context.Context, string) (model.AgentResult, error) {
	return model.AgentResult{Text: "{}", TokensIn: 100, TokensOut: 20, CostMicros: 5000, CostSource: "measured"}, nil
}

func TestRunEnvAccountNamesTheAccountThatGotTheEnv(t *testing.T) {
	a := sealedAgents(t)
	ctx := context.Background()
	in := deepseekInput()
	api, err := a.CreateAccount(ctx, &model.User{ID: "u1"}, "claude", "DeepSeek", &in)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Store.SetRoleAccounts(ctx, "u1", "claude", api.Account.ID, []string{"builder"}); err != nil {
		t.Fatal(err)
	}
	env, _, acct, err := a.RunEnvAccount(ctx, "claude", "u1", model.AgentBuilder)
	if err != nil || len(env) == 0 || acct.AccountID != api.Account.ID || acct.Host == "" || acct.Models == nil {
		t.Fatalf("%v %v %+v", err, env, acct)
	}
	if _, _, acct, _ = a.RunEnvAccount(ctx, "claude", "", model.AgentBuilder); acct.AccountID != "" {
		t.Fatalf("no user, no account: %+v", acct)
	}
}

func TestRunAccountFollowsTheRoleAccount(t *testing.T) {
	a := sealedAgents(t)
	ctx := context.Background()
	u := &model.User{ID: "u1"}
	in := deepseekInput()
	api, err := a.CreateAccount(ctx, u, "claude", "DeepSeek", &in)
	if err != nil {
		t.Fatal(err)
	}
	login, err := a.CreateAccount(ctx, u, "claude", "Work", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Store.SetRoleAccounts(ctx, "u1", "claude", api.Account.ID, []string{"builder"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.SetRoleAccounts(ctx, "u1", "claude", login.Account.ID, []string{"inspector"}); err != nil {
		t.Fatal(err)
	}
	b := a.RunAccount(ctx, "claude", "u1", model.AgentBuilder)
	if b.AccountID != api.Account.ID || b.Host != "api.deepseek.com" || b.Agent != "claude" || b.Models == nil {
		t.Fatalf("builder: %+v", b)
	}
	i := a.RunAccount(ctx, "claude", "u1", model.AgentInspector)
	if i.AccountID != login.Account.ID || i.Host != "" || i.Models != nil {
		t.Fatalf("inspector: %+v", i)
	}
	if s := a.RunAccount(ctx, "claude", "u1", model.AgentArchitect); s.AccountID != SystemAccountID {
		t.Fatalf("unassigned role falls to the system default: %+v", s)
	}
	if s := a.RunAccount(ctx, "codex", "u1", model.AgentBuilder); s.AccountID != SystemAccountID || s.Agent != "codex" {
		t.Fatalf("codex: %+v", s)
	}
	if n := a.RunAccount(ctx, "claude", "", model.AgentBuilder); n.AccountID != "" {
		t.Fatalf("no user: %+v", n)
	}
}

func TestStampDropsCostOnAPIAccountsOnly(t *testing.T) {
	m := APIModels{Opus: "deepseek-flash[1m]", Sonnet: "deepseek-v4-pro"}
	u := model.UsageRecord{Model: "opus[1m]", CostMicros: 9000, Source: "measured"}
	RunAccount{Agent: "claude", AccountID: "acc1", Host: "api.deepseek.com", Models: &m}.Stamp(&u)
	if u.CostMicros != 0 || u.Source != "unknown" || u.ServedModel != "deepseek-flash[1m]" || u.ProviderHost != "api.deepseek.com" || u.AccountID != "acc1" {
		t.Fatalf("api: %+v", u)
	}
	l := model.UsageRecord{Model: "opus", CostMicros: 9000, Source: "measured"}
	RunAccount{Agent: "codex", AccountID: SystemAccountID}.Stamp(&l)
	if l.CostMicros != 9000 || l.Source != "measured" || l.ServedModel != "" || l.ProviderHost != "" || l.AccountID != SystemAccountID {
		t.Fatalf("login: %+v", l)
	}
}

func TestFactoryRunRecordsTheAccountPickedAtStart(t *testing.T) {
	h := &costHarness{}
	f, wo, _ := monitorFactory(t, h)
	rs := &recStore{}
	f.Usage = NewUsageTracker(rs)
	m := APIModels{Opus: "deepseek-flash[1m]"}
	accts := map[model.AgentRole]RunAccount{
		model.AgentBuilder:   {Agent: "claude", AccountID: "api1", Host: "api.deepseek.com", Models: &m},
		model.AgentInspector: {Agent: "codex", AccountID: "cx1"},
	}
	f.RunEnv = func(_ context.Context, role model.AgentRole, _ *model.WorkOrder) ([]string, []string, RunAccount, error) {
		return nil, nil, accts[role], nil
	}

	var wg sync.WaitGroup
	for _, role := range []model.AgentRole{model.AgentBuilder, model.AgentInspector} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := f.runAgent(context.Background(), wo, role, "anthropic", "opus", "go", nil, "", runOpts{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got := map[string]model.UsageRecord{}
	for _, r := range rs.recs {
		got[r.Role] = r
	}
	b, i := got["builder"], got["inspector"]
	if b.AccountID != "api1" || b.ServedModel != "deepseek-flash[1m]" || b.ProviderHost != "api.deepseek.com" || b.CostMicros != 0 || b.Source != "unknown" {
		t.Fatalf("builder: %+v", b)
	}
	if i.AccountID != "cx1" || i.Agent != "codex" || i.ProviderHost != "" || i.CostMicros != 5000 || i.Source != "measured" {
		t.Fatalf("inspector: %+v", i)
	}
	runs, err := f.Runs.(*store.RunRepo).List(context.Background(), wo.ID)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs: %v %+v", err, runs)
	}
	var zero, full int
	for _, r := range runs {
		switch r.CostMicros {
		case 0:
			zero++
		case 5000:
			full++
		}
	}
	if zero != 1 || full != 1 {
		t.Fatalf("the API run must store no cost in the runs table and the login run its own: %+v", runs)
	}
}

func TestChatRecordsTheAccountAndHidesAPICost(t *testing.T) {
	ctx := context.Background()
	r := newChatRig(t, &chatPlain{reply: "ok"})
	m := APIModels{Opus: "deepseek-flash"}
	r.chat.Account = func(_ context.Context, userID string) RunAccount {
		if userID != "ana" {
			t.Errorf("account asked for %q", userID)
		}
		return RunAccount{Agent: "claude", AccountID: "api1", Host: "api.deepseek.com", Models: &m}
	}
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	evs, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	u := evs[len(evs)-1].Message.Usage
	if u.ServedModel != "deepseek-flash" || u.ProviderHost != "api.deepseek.com" || u.CostMicros != 0 {
		t.Fatalf("message usage: %+v", u)
	}
	r.usage.mu.Lock()
	defer r.usage.mu.Unlock()
	if len(r.usage.recs) == 0 {
		t.Fatal("nothing recorded")
	}
	for _, rec := range r.usage.recs {
		if rec.AccountID != "api1" || rec.Agent != "claude" || rec.ProviderHost != "api.deepseek.com" || rec.CostMicros != 0 || rec.Source != "unknown" {
			t.Fatalf("every chat record carries the account: %+v", rec)
		}
	}
}

func TestUserSummaryByAccount(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	repo := &store.UsageRepo{DB: db}
	users := &store.UserRepo{DB: db}
	for _, id := range []string{"u1", "u2"} {
		if err := users.Create(ctx, model.User{ID: id, Email: id + "@t", Name: id, Role: model.RoleEngineer}); err != nil {
			t.Fatal(err)
		}
	}
	accs := &store.AgentAccountRepo{DB: db}
	for _, a := range []model.AgentAccount{
		{ID: "api1", UserID: "u1", Agent: "claude", Label: "DeepSeek"},
		{ID: "sub1", UserID: "u1", Agent: "claude", Label: "Work"},
		{ID: "other", UserID: "u2", Agent: "claude", Label: "Theirs"},
	} {
		if err := accs.Create(ctx, &a); err != nil {
			t.Fatal(err)
		}
	}
	jkt, _ := time.LoadLocation("Asia/Jakarta")
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, jkt)
	n := 0
	add := func(user string, at time.Time, r model.UsageRecord) {
		n++
		r.ID, r.UserID, r.Role, r.CreatedAt = string(rune('a'+n)), user, "builder", at
		if r.Provider == "" {
			r.Provider, r.Model, r.Source = "anthropic", "opus", "measured"
		}
		if err := repo.Insert(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	h := now.Add(-time.Hour)
	add("u1", h, model.UsageRecord{Agent: "claude", AccountID: "api1", ServedModel: "deepseek-flash[1m]", ProviderHost: "api.deepseek.com", TokensIn: 100, TokensOut: 10, Source: "unknown"})
	add("u1", h, model.UsageRecord{Agent: "claude", AccountID: "api1", ServedModel: "deepseek-chat", ProviderHost: "api.deepseek.com", TokensIn: 50, TokensOut: 5, Source: "unknown"})
	add("u1", h, model.UsageRecord{Agent: "claude", AccountID: "sub1", TokensIn: 7, TokensOut: 1, CostMicros: 700})
	add("u1", h, model.UsageRecord{Agent: "codex", AccountID: SystemAccountID, TokensIn: 3, TokensOut: 1, CostMicros: 30})
	add("u1", h, model.UsageRecord{Agent: "claude", AccountID: "gone", TokensIn: 2, TokensOut: 2, CostMicros: 20})
	add("u1", h, model.UsageRecord{AccountID: UntrackedAccountID, TokensIn: 1, TokensOut: 1, CostMicros: 10})
	add("u1", h, model.UsageRecord{TokensIn: 4, TokensOut: 1, CostMicros: 40})
	add("u1", now.AddDate(0, 0, -20), model.UsageRecord{Agent: "claude", AccountID: "sub1", TokensIn: 1000, TokensOut: 1, CostMicros: 1})
	add("u2", h, model.UsageRecord{Agent: "claude", AccountID: "other", TokensIn: 999, TokensOut: 1, CostMicros: 99})

	tr := NewUsageTracker(repo)
	s, err := tr.UserSummary(ctx, "u1", 7, now, jkt)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]model.UsageAccount{}
	for _, a := range s.Accounts {
		by[a.Agent+"/"+a.AccountID] = a
	}
	if len(by) != 6 {
		t.Fatalf("rows: %+v", s.Accounts)
	}
	api := by["claude/api1"]
	if api.Label != "DeepSeek" || api.Kind != "api" || api.Host != "api.deepseek.com" || api.Runs != 2 || api.TokensIn != 150 || api.TokensOut != 15 ||
		api.CostMicros != nil || len(api.ServedModels) != 2 || api.ServedModels[0] != "deepseek-chat" {
		t.Fatalf("api: %+v", api)
	}
	sub := by["claude/sub1"]
	if sub.Label != "Work" || sub.Kind != "subscription" || sub.Runs != 1 || sub.CostMicros == nil || *sub.CostMicros != 700 || len(sub.ServedModels) != 0 {
		t.Fatalf("sub: %+v", sub)
	}
	if sys := by["codex/"+SystemAccountID]; sys.Label != "System default" || sys.Kind != "subscription" {
		t.Fatalf("system: %+v", sys)
	}
	if g := by["claude/gone"]; g.Label != "Deleted account" || !g.Deleted {
		t.Fatalf("deleted: %+v", g)
	}
	if o := by["/"+UntrackedAccountID]; o.Label != "Before account tracking" || !o.Untracked || o.CostMicros == nil || *o.CostMicros != 10 {
		t.Fatalf("old rows: %+v", o)
	}
	if u := by["/"]; u.Label != "Unknown account" || u.Untracked || u.CostMicros == nil || *u.CostMicros != 40 {
		t.Fatalf("new rows without an account: %+v", u)
	}
	if !s.MonthToDate.CostPartial {
		t.Fatalf("month to date holds API rows: %+v", s.MonthToDate)
	}
	for _, r := range s.Roles {
		if r.Role == "builder" && !r.CostPartial {
			t.Fatalf("builder role holds API rows: %+v", r)
		}
	}
	var runs int
	for _, r := range s.Roles {
		runs += r.Runs
	}
	if runs != 7 {
		t.Fatalf("roles still count every run: %d", runs)
	}
	if _, ok := by["claude/other"]; ok {
		t.Fatal("another user's account leaked")
	}
	wide, _ := tr.UserSummary(ctx, "u1", 30, now, jkt)
	for _, a := range wide.Accounts {
		if a.AccountID == "sub1" && a.TokensIn != 1007 {
			t.Fatalf("30-day sub: %+v", a)
		}
	}
}
