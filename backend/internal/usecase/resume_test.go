package usecase

import (
	"context"
	"testing"

	"legos-factory/backend/internal/model"
)

func TestResumeOnStartRequeuesAnInterruptedVerification(t *testing.T) {
	ctx := context.Background()
	f, wo, wos := monitorFactory(t, nil)
	q := &fakeQueue{}
	f.Sched = q
	if err := wos.UpdateState(ctx, wo.ID, model.StateVerifying); err != nil {
		t.Fatal(err)
	}
	if err := f.ResumeOnStart(ctx); err != nil {
		t.Fatal(err)
	}
	if len(q.enqueued) != 1 || q.enqueued[0] != "verify:" {
		t.Fatalf("enqueued %v, want one verify job", q.enqueued)
	}
}
