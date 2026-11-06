package usecase

import (
	"context"
	"encoding/json"
	"time"

	"legos-factory/backend/internal/model"
)

// Outbound dependencies of the usecase layer. Each interface lists only the
// methods usecases call; concrete implementations live in store, scheduler,
// eventbus and usage, and are wired in app.

type WorkOrderStore interface {
	Create(ctx context.Context, wo *model.WorkOrder) error
	Get(ctx context.Context, id string) (*model.WorkOrder, error)
	List(ctx context.Context) ([]model.WorkOrder, error)
	PRTracking(ctx context.Context) ([]model.WorkOrder, error)
	Delete(ctx context.Context, wo *model.WorkOrder) error
	UpdateState(ctx context.Context, id string, s model.State) error
	UpdateFields(ctx context.Context, id string, fields map[string]any) error
	UpdateStateAndFields(ctx context.Context, id string, s model.State, fields map[string]any) error
	ReplacePlan(ctx context.Context, woID string, tasks []model.Task) error
	AppendPlan(ctx context.Context, woID string, tasks []model.Task) error
	Plan(ctx context.Context, woID string) ([]model.Task, error)
	AppendEvent(ctx context.Context, woID, actor, kind, message string, payload map[string]any) (*model.Event, error)
	Events(ctx context.Context, woID string, afterID int64, limit int) ([]model.Event, error)
	LatestEvent(ctx context.Context, woID, kind string) (*model.Event, error)
	EventsByKind(ctx context.Context, woID string, kinds []string) ([]model.Event, error)
	ProjectEvents(ctx context.Context, projectID, kind string, limit int) ([]model.Event, error)
}

type ProjectStore interface {
	Get(ctx context.Context, id string) (*model.Project, error)
	List(ctx context.Context) ([]model.Project, error)
	Create(ctx context.Context, p *model.Project) error
	SetBinding(ctx context.Context, id, connectionID, remoteRepo string) error
	SetLanguage(ctx context.Context, id, language string) error
	SetKBScope(ctx context.Context, id string, include, exclude []string) error
	DeleteWithOrders(ctx context.Context, id string, orders []model.WorkOrder) error
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
	Since(ctx context.Context, userID string, since time.Time) ([]model.UsageRecord, error)
}

type ChatStore interface {
	Create(ctx context.Context, c *model.Chat) error
	Get(ctx context.Context, id string) (*model.Chat, error)
	List(ctx context.Context, userID string) ([]model.Chat, error)
	Update(ctx context.Context, id, title, modelRef string) error
	Rename(ctx context.Context, id, title string) error
	RenameIf(ctx context.Context, id, from, to string) (bool, error)
	SetProject(ctx context.Context, id, projectID string) error
	Touch(ctx context.Context, id string) error
	Delete(ctx context.Context, id string) error
	AddMessage(ctx context.Context, m *model.ChatMessage) error
	Messages(ctx context.Context, chatID string) ([]model.ChatMessage, error)
}

type SettingsReader interface {
	Get(ctx context.Context) (*model.Settings, error)
}

// Sandbox owns work-order workspaces: preparing them (cloning with auth when
// given), running checks, committing verified changes locally (returning the
// SHA, "" when not under version control) and pushing a branch to origin.
type Sandbox interface {
	Prepare(ctx context.Context, woID, repoURL string, auth *model.GitAuth) (string, error)
	Destroy(ctx context.Context, woID string) error
	RunCheck(ctx context.Context, dir string, check model.Check, timeout time.Duration) model.CheckResult
	Commit(ctx context.Context, dir, message string) (string, error)
	PushBranch(ctx context.Context, dir, branch string, auth *model.GitAuth) (base string, err error)
	ChangedFiles(ctx context.Context, dir string) ([]string, error)
	Diff(ctx context.Context, dir string) ([]model.FileDiff, error)
	CommitDiff(ctx context.Context, dir, sha string) (model.WODiff, error)
	TaskWorktree(ctx context.Context, woDir, taskDir, branch string) (ok bool, err error)
	MergeTask(ctx context.Context, woDir, branch, message string) (conflict bool, err error)
	SyncTask(ctx context.Context, woDir, taskDir string) (conflicted []string, err error)
	RemoveTaskWorktree(ctx context.Context, woDir, taskDir, branch string) error
}

type ConnectionStore interface {
	Create(ctx context.Context, c *model.Connection, sealed []byte) error
	Get(ctx context.Context, id string) (*model.Connection, error)
	Secret(ctx context.Context, id string) ([]byte, error)
	Visible(ctx context.Context, userID string) ([]model.Connection, error)
	Rename(ctx context.Context, id, name string) error
	SetStatus(ctx context.Context, id, status string) error
	RotateSecret(ctx context.Context, id string, sealed []byte) error
	RecordCheck(ctx context.Context, id, account, checkErr string) error
	Delete(ctx context.Context, id string) error
}

// Sealer encrypts credentials at rest.
type Sealer interface {
	Seal(plain []byte) ([]byte, error)
	Open(sealed []byte) ([]byte, error)
}

// GitProvider opens pull requests on a hosted git service.
type GitProvider interface {
	CheckAuth(ctx context.Context) (account string, err error)
	OpenPullRequest(ctx context.Context, pr model.PRRequest) (model.PullRequest, error)
	PullRequestStatus(ctx context.Context, ref string) (model.PRStatus, error)
	GitAuth() model.GitAuth
}

// IssueTracker reads issues; it never modifies them.
type IssueTracker interface {
	CheckAuth(ctx context.Context) (account string, err error)
	GetIssue(ctx context.Context, key string) (model.Issue, error)
}

type RunStore interface {
	Start(ctx context.Context, id, woID, taskID, kind, harness string) error
	Complete(ctx context.Context, id string, res model.AgentResult) error
	Fail(ctx context.Context, id, errMsg string) error
	Wait(ctx context.Context, id, question string, res model.AgentResult) error
	CompletedBuilderTasks(ctx context.Context, woID string) (map[string]bool, error)
	LatestRemediationState(ctx context.Context, woID string) (string, error)
}

type FindingStore interface {
	Create(ctx context.Context, f model.Finding) error
	ResolveOpen(ctx context.Context, woID string) error
	ResolveOpenExcept(ctx context.Context, woID string, runIDs []string) error
	CountOpenBlockers(ctx context.Context, woID string) (int, error)
	List(ctx context.Context, woID string) ([]model.Finding, error)
	Get(ctx context.Context, woID, id string) (*model.Finding, error)
	SetDisposition(ctx context.Context, id, status, note, by string) error
}

type QualityStore interface {
	Save(ctx context.Context, c model.QualityCheck) error
	Count(ctx context.Context, woID string) (int, error)
	CountForRevision(ctx context.Context, woID string, revision int) (int, error)
	ForRevision(ctx context.Context, woID string, revision int) ([]model.QualityCheck, error)
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

// Steerer is a Harness that accepts a message into a running turn.
type Steerer interface {
	Steer(ctx context.Context, sessionID, text string) error
}

// SessionHarness is a Harness that can keep a session open after a run (a
// request with Keep set) and take further prompts in it, so an agent keeps its
// own context across prompts. Continue returns model.ErrAgentSessionNotFound
// when the session is gone; callers then start over.
type SessionHarness interface {
	Continue(ctx context.Context, sessionID string, req model.AgentRequest) (model.AgentSession, error)
	Close(sessionID string)
}

type Publisher interface {
	Publish(e model.LiveEvent)
}

type Budget interface {
	Reserve(estMicros int64) (release func(actual int64), ok bool)
	Record(ctx context.Context, r model.UsageRecord) error
}

// Judge asks Jev typed yes/no questions about a state. Answers are
// probabilities keyed by question id.
type Judge interface {
	Ask(ctx context.Context, state any, questions map[string]Noul) (JudgeResult, error)
}

type Noul struct {
	Instructions string        `json:"instructions"`
	Criteria     *NoulCriteria `json:"criteria,omitempty"`
}

type NoulCriteria struct {
	True  string `json:"true"`
	False string `json:"false"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// JudgeResult carries the exact request body that was (or would have been)
// sent, so the audit event can replay the decision. Request and
// RequestedModel are set even when Ask fails.
type JudgeResult struct {
	Answers        map[string]float64
	Model          string // model that answered
	RequestedModel string
	Request        json.RawMessage
	Usage          Usage
}

type CodeFactStore interface {
	Add(ctx context.Context, woID string, facts []model.CodeFact, max int) error
	List(ctx context.Context, woID string) ([]model.CodeFact, error)
}
