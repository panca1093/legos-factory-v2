package usecase

import (
	"context"

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
}

type Queue interface {
	Enqueue(ctx context.Context, kind, woID, taskID string, payload map[string]any) (string, error)
	CancelWO(ctx context.Context, woID string) error
	HasActive(ctx context.Context, woID, taskID, kind string) bool
}

type Publisher interface {
	Publish(e model.LiveEvent)
}

type Budget interface {
	Reserve(estMicros int64) (release func(actual int64), ok bool)
	Record(ctx context.Context, r model.UsageRecord) error
}
