package engine

import (
	"context"
	"errors"
	"testing"
)

func TestFollowUpNeedsAPullRequestBranchUnlessMerged(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t, "echo boom; false")
	parent := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, parent); err != nil {
		t.Fatal(err)
	}
	if c := failedClass(t, ev.e, parent); c != "verify" {
		t.Fatalf("class = %q", c)
	}
	if _, err := ev.e.FollowUp(ctx, ev.p, parent, "x"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("follow-up of a parent that failed before its PR = %v, want ErrNotAllowed", err)
	}
}
