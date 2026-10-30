package fact

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

type step struct {
	kind    string
	payload any
}

func mustJSON(v any) []byte {
	if v == nil {
		return []byte("{}")
	}
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func mk(steps ...step) []Fact {
	base := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	var out []Fact
	for i, s := range steps {
		out = append(out, Fact{
			ID: int64(i + 1), Subject: "wo_1", Kind: s.kind,
			Payload: mustJSON(s.payload), At: base.Add(time.Duration(i) * time.Second),
		})
	}
	return out
}

func TestDerive(t *testing.T) {
	created := step{KindWOCreated, WOCreated{}}
	run := step{KindRunStarted, RunStarted{Run: "r1", Role: "builder"}}
	open := step{KindPROpened, PROpened{Number: 1, HeadSHA: "aaa"}}
	requeue := step{KindWORequeued, nil}
	chk := func(name, state, sha string) step {
		return step{KindCheckObserved, CheckObserved{Name: name, State: state, HeadSHA: sha}}
	}
	stable := func(sha string) step { return step{KindChecksStable, ChecksStable{HeadSHA: sha}} }
	tests := []struct {
		name  string
		steps []step
		want  Status
	}{
		{"empty", nil, StatusQueued},
		{"created", []step{created}, StatusQueued},
		{"building", []step{created, run}, StatusBuilding},
		{"verifying", []step{created, run, {KindTaskCommitted, TaskCommitted{}}, {KindVerifyPassed, nil}}, StatusVerifying},
		{"verify failed goes back to building", []step{created, run, {KindVerifyPassed, nil}, {KindVerifyFailed, VerifyFailed{}}}, StatusBuilding},
		{"pr open", []step{created, run, open}, StatusPROpen},
		{"pending only", []step{created, open, chk("ci", "pending", "aaa")}, StatusPROpen},
		{"all success without stable is not green", []step{created, open, chk("ci", "success", "aaa"), chk("lint", "success", "aaa")}, StatusPROpen},
		{"green", []step{created, open, chk("ci", "success", "aaa"), chk("lint", "success", "aaa"), stable("aaa")}, StatusCIGreen},
		{"late check voids stable", []step{created, open, chk("ci", "success", "aaa"), stable("aaa"), chk("e2e", "pending", "aaa")}, StatusPROpen},
		{"stable for other head ignored", []step{created, open, chk("ci", "success", "aaa"), stable("zzz")}, StatusPROpen},
		{"stable after push is voided", []step{created, open, chk("ci", "success", "aaa"), stable("aaa"), {KindPRPushed, PRPushed{HeadSHA: "bbb"}}}, StatusPROpen},
		{"failure beats stable", []step{created, open, chk("ci", "failure", "aaa"), stable("aaa")}, StatusCIFailed},
		{"failure wins", []step{created, open, chk("ci", "failure", "aaa"), chk("lint", "success", "aaa")}, StatusCIFailed},
		{"check state is latest per name", []step{created, open, chk("ci", "failure", "aaa"), chk("ci", "success", "aaa"), stable("aaa")}, StatusCIGreen},
		{"other head ignored", []step{created, open, chk("ci", "failure", "zzz")}, StatusPROpen},
		{"requeue after ci_failed", []step{created, open, chk("ci", "failure", "aaa"), requeue}, StatusBuilding},
		{"stale check after requeue ignored", []step{created, open, chk("ci", "failure", "aaa"), requeue, chk("ci", "failure", "aaa")}, StatusBuilding},
		{"push resets checks", []step{created, open, chk("ci", "failure", "aaa"), requeue, run, {KindPRPushed, PRPushed{HeadSHA: "bbb"}}}, StatusPROpen},
		{"new head green", []step{created, open, chk("ci", "failure", "aaa"), {KindPRPushed, PRPushed{HeadSHA: "bbb"}}, chk("ci", "failure", "aaa"), chk("ci", "success", "bbb"), stable("bbb")}, StatusCIGreen},
		{"merged", []step{created, open, chk("ci", "success", "aaa"), {KindPRMerged, nil}}, StatusMerged},
		{"closed", []step{created, open, {KindPRClosed, nil}}, StatusClosed},
		{"failed", []step{created, run, {KindWOFailed, WOFailed{Class: "out_of_funds"}}}, StatusFailed},
		{"cancelled", []step{created, run, {KindWOCancelled, nil}}, StatusCancelled},
		{"terminal is final", []step{created, {KindWOFailed, WOFailed{}}, run, requeue}, StatusFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Derive(mk(tc.steps...)); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestStoreRoundTripAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "facts.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seq := []step{
		{KindWOCreated, WOCreated{Objective: "x"}},
		{KindPROpened, PROpened{Number: 7, URL: "u", Branch: "b", HeadSHA: "aaa"}},
		{KindCheckObserved, CheckObserved{Name: "ci", State: "failure", HeadSHA: "aaa"}},
	}
	var prev int64
	for _, st := range seq {
		f, err := s.Append(ctx, "wo_1", st.kind, st.payload)
		if err != nil {
			t.Fatal(err)
		}
		if f.ID <= prev {
			t.Fatalf("ids not increasing: %d after %d", f.ID, prev)
		}
		prev = f.ID
	}
	if _, err := s.Append(ctx, "wo_2", KindWOCreated, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, "wo_1", KindWORequeued, nil); err != nil {
		t.Fatal(err)
	}

	facts, err := s.List(ctx, "wo_1")
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []string{KindWOCreated, KindPROpened, KindCheckObserved, KindWORequeued}
	if len(facts) != len(wantKinds) {
		t.Fatalf("got %d facts", len(facts))
	}
	for i, k := range wantKinds {
		if facts[i].Kind != k {
			t.Fatalf("fact %d kind %s want %s", i, facts[i].Kind, k)
		}
	}
	po, err := Decode[PROpened](facts[1])
	if err != nil || po.Number != 7 || po.HeadSHA != "aaa" {
		t.Fatalf("decode: %+v %v", po, err)
	}
	before := Derive(facts)
	if before != StatusBuilding {
		t.Fatalf("status %s", before)
	}
	subs, err := s.Subjects(ctx)
	if err != nil || len(subs) != 2 || subs[0] != "wo_1" || subs[1] != "wo_2" {
		t.Fatalf("subjects %v %v", subs, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	again, err := s2.List(ctx, "wo_1")
	if err != nil {
		t.Fatal(err)
	}
	if got := Derive(again); got != before {
		t.Fatalf("after restart %s want %s", got, before)
	}
	if empty, _ := s2.List(ctx, "nope"); len(empty) != 0 {
		t.Fatal("expected no facts")
	}
}

func TestStalled(t *testing.T) {
	base := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	f := func(kind string, payload any, d time.Duration) Fact {
		return Fact{Kind: kind, Payload: mustJSON(payload), At: base.Add(d)}
	}
	started := f(KindRunStarted, RunStarted{Run: "r1"}, 0)
	tests := []struct {
		name  string
		facts []Fact
		now   time.Duration
		want  bool
	}{
		{"no run", nil, 0, false},
		{"fresh run", []Fact{started}, 2 * time.Minute, false},
		{"silent run", []Fact{started}, 4 * time.Minute, true},
		{"recent step", []Fact{started, f(KindRunStep, RunStep{Run: "r1"}, 3*time.Minute)}, 5 * time.Minute, false},
		{"old step", []Fact{started, f(KindRunStep, RunStep{Run: "r1"}, time.Minute)}, 5 * time.Minute, true},
		{"other run's step ignored", []Fact{started, f(KindRunStep, RunStep{Run: "r0"}, 4*time.Minute)}, 5 * time.Minute, true},
		{"finished", []Fact{started, f(KindRunFinished, RunFinished{Run: "r1"}, time.Minute)}, time.Hour, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Stalled(tc.facts, base.Add(tc.now)); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestAppendBatchIsAtomic(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.AppendBatch(ctx, "wo-1", []NewFact{{"a", map[string]int{"n": 1}}, {"b", nil}})
	if err != nil || len(got) != 2 || got[0].ID >= got[1].ID {
		t.Fatalf("batch = %+v, %v", got, err)
	}
	if _, err := s.AppendBatch(ctx, "wo-1", []NewFact{{"c", nil}, {"bad", make(chan int)}}); err == nil {
		t.Fatal("unmarshalable payload accepted")
	}
	all, _ := s.List(ctx, "wo-1")
	if len(all) != 2 {
		t.Fatalf("failed batch left %d facts, want 2", len(all))
	}
}
