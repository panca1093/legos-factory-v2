package usecase

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

func TestAnswerRequeuesTheJobInsteadOfLoopingInIt(t *testing.T) {
	r, h := askRig(t, map[model.AgentRole][]string{
		model.AgentBuilder:   {askHalfUp, "rounded"},
		model.AgentArchitect: {archAnswer},
	})
	err := r.f.handleImplement(context.Background(), model.Job{WOID: r.wo.ID, TaskID: "T1"})
	if !errors.Is(err, model.ErrRequeue) || h.runs(model.AgentBuilder) != 1 {
		t.Fatalf("err = %v, builder runs = %d; one run per job, then a requeue", err, h.runs(model.AgentBuilder))
	}
}

func TestNeedsContextRunShowsAsWaiting(t *testing.T) {
	r, _ := askRig(t, map[model.AgentRole][]string{
		model.AgentBuilder:   {askHalfUp},
		model.AgentArchitect: {archAnswer},
	})
	_ = r.f.handleImplement(context.Background(), model.Job{WOID: r.wo.ID, TaskID: "T1"})
	runs, _ := r.f.Runs.(*store.RunRepo).List(context.Background(), r.wo.ID)
	var state string
	for _, run := range runs {
		if run.Kind == "builder" {
			state = run.State
		}
	}
	var closed bool
	for _, e := range r.events(t, "agent_completed") {
		closed = closed || e.Payload["needs_context"] == true
	}
	if state != "waiting" || !closed {
		t.Fatalf("run state %q, agent_completed with needs_context = %v", state, closed)
	}
}

func TestBareMarkerFailsTheRunWithoutAskingAnyone(t *testing.T) {
	r, h := askRig(t, map[model.AgentRole][]string{model.AgentBuilder: {"working\nNEEDS_CONTEXT:"}})
	err := r.f.handleImplement(context.Background(), model.Job{WOID: r.wo.ID, TaskID: "T1"})
	if !errors.Is(err, model.ErrPermanent) || h.runs(model.AgentArchitect) != 0 || r.doneTasks(t) != 0 {
		t.Fatalf("err = %v, architect runs = %d, done = %d", err, h.runs(model.AgentArchitect), r.doneTasks(t))
	}
}

func TestRepeatFromTheSameTaskGoesToAHuman(t *testing.T) {
	r, h := askRig(t, map[model.AgentRole][]string{model.AgentBuilder: {askHalfUp}})
	r.setLog(t, `[{"question":"`+halfUp+`","answer":"Half up.","by":"architect","task":"T1"}]`)
	r.implement(t)
	wo := r.reload(t)
	if wo.State != model.StateWaitingForHuman || h.runs(model.AgentArchitect) != 0 || h.runs(model.AgentForeman) != 0 {
		t.Fatalf("state %s, architect %d, foreman %d; an answer the Builder already had did not help", wo.State, h.runs(model.AgentArchitect), h.runs(model.AgentForeman))
	}
}

func TestParallelAnswersKeepEachOther(t *testing.T) {
	r, _ := askRig(t, nil)
	ctx := context.Background()
	var wg sync.WaitGroup
	for _, task := range []string{"T1", "T2", "T3"} {
		stale := r.reload(t) // every job holds the order as it was before any answer
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.f.recordBuildAnswer(ctx, stale, task, "q "+task, "a "+task, "why", "architect", "", true); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	wo := r.reload(t)
	if len(wo.Clarifications) != 3 {
		t.Fatalf("clarifications = %+v", wo.Clarifications)
	}
	for _, task := range []string{"T1", "T2", "T3"} {
		if !strings.Contains(wo.Spec, "Amendment: q "+task+" → a "+task) {
			t.Fatalf("amendment for %s lost: %q", task, wo.Spec)
		}
	}
}

func TestSecondHumanQuestionDoesNotReplaceTheFirst(t *testing.T) {
	r, _ := askRig(t, nil)
	ctx := context.Background()
	if err := r.wos.UpdateState(ctx, r.wo.ID, model.StateImplementing); err != nil {
		t.Fatal(err)
	}
	first, second := r.reload(t), r.reload(t)
	if err := r.f.parkForHuman(ctx, first, model.Job{TaskID: "T1"}, "builder", "q1?"); err != nil {
		t.Fatal(err)
	}
	if err := r.f.parkForHuman(ctx, second, model.Job{TaskID: "T2"}, "builder", "q2?"); err != nil {
		t.Fatal(err)
	}
	wo := r.reload(t)
	if wo.State != model.StateWaitingForHuman || wo.ClarifQuestion != "q1?" || wo.ClarifTask != "T1" {
		t.Fatalf("state %s question %q task %q", wo.State, wo.ClarifQuestion, wo.ClarifTask)
	}
}
