package usecase

import (
	"context"
	"time"

	"legos-factory/backend/internal/model"
)

// Outbound dependencies of the usecase layer. Each interface lists only the
// methods usecases call; concrete implementations live in store, scheduler,
// eventbus and usage, and are wired in app.

type WorkOrderStore interface {
	Get(ctx context.Context, id string) (*model.WorkOrder, error)
	List(ctx context.Context) ([]model.WorkOrder, error)
	UpdateState(ctx context.Context, id string, s model.State) error
	UpdateFields(ctx context.Context, id string, fields map[string]any) error
	ReplacePlan(ctx context.Context, woID string, tasks []model.Task) error
	Plan(ctx context.Context, woID string) ([]model.Task, error)
	AppendEvent(ctx context.Context, woID, actor, kind, message string, payload map[string]any) (*model.Event, error)
}

type ProjectStore interface {
	Get(ctx context.Context, id string) (*model.Project, error)
	List(ctx context.Context) ([]model.Project, error)
	Create(ctx context.Context, p *model.Project) error
}

type MemberStore interface {
	Add(ctx context.Context, projectID, userID string) error
	Remove(ctx context.Context, projectID, userID string) error
	IsMember(ctx context.Context, projectID, userID string) (bool, error)
	ProjectIDsFor(ctx context.Context, userID string) ([]string, error)
	List(ctx context.Context, projectID string) ([]model.ProjectMember, error)
}

type UserDirectory interface {
	ByEmail(ctx context.Context, email string) (*model.User, error)
}

type UserStore interface {
	UserDirectory
	EnsureUser(ctx context.Context, u model.User) error
	Create(ctx context.Context, u model.User) error
	CreateSession(ctx context.Context, sid, userID string, expiresAt time.Time) error
	UserForSession(ctx context.Context, sid string) (*model.User, error)
	DeleteSession(ctx context.Context, sid string) error
}

type UsageStore interface {
	Insert(ctx context.Context, u model.UsageRecord) error
	Totals(ctx context.Context) (model.UsageTotals, error)
	SpentMicros(ctx context.Context) int64
}

type KnowledgeStore interface {
	Insert(ctx context.Context, k *model.KnowledgeRecord) error
	Get(ctx context.Context, id string) (*model.KnowledgeRecord, error)
	Search(ctx context.Context, q model.KnowledgeQuery) ([]model.KnowledgeRecord, error)
}

type SettingsReader interface {
	Get(ctx context.Context) (*model.Settings, error)
}

// Sandbox executes verification checks in a workspace and commits verified
// changes locally, returning the SHA ("" when not under version control).
type Sandbox interface {
	RunCheck(ctx context.Context, dir string, check model.Check, timeout time.Duration) model.CheckResult
	Commit(ctx context.Context, dir, message string) (string, error)
}

type RunStore interface {
	Start(ctx context.Context, id, woID, taskID, kind, harness string) error
	Complete(ctx context.Context, id string, res model.AgentResult) error
	Fail(ctx context.Context, id, errMsg string) error
	CompletedBuilderTasks(ctx context.Context, woID string) (map[string]bool, error)
}

type FindingStore interface {
	Create(ctx context.Context, f model.Finding) error
	ResolveOpen(ctx context.Context, woID string) error
	CountOpenBlockers(ctx context.Context, woID string) (int, error)
}

type QualityStore interface {
	Save(ctx context.Context, c model.QualityCheck) error
	Count(ctx context.Context, woID string) (int, error)
	CountForRevision(ctx context.Context, woID string, revision int) (int, error)
	AttachCommit(ctx context.Context, woID string, revision int, sha string) error
}

type Queue interface {
	Enqueue(ctx context.Context, kind, woID, taskID string, payload map[string]any) (string, error)
	CancelWO(ctx context.Context, woID string) error
	HasActive(ctx context.Context, woID, taskID, kind string) bool
}

// Harness abstracts the coding-agent runtime (OpenCode, mock, ...).
type Harness interface {
	Start(ctx context.Context, req model.AgentRequest) (model.AgentSession, error)
	Cancel(ctx context.Context, sessionID string) error
	Result(ctx context.Context, sessionID string) (model.AgentResult, error)
	Name() string
}

type Publisher interface {
	Publish(e model.LiveEvent)
}

type Budget interface {
	Reserve(estMicros int64) (release func(actual int64), ok bool)
	Record(ctx context.Context, r model.UsageRecord) error
}
