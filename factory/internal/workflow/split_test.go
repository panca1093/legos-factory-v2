package workflow

import (
	"reflect"
	"strings"
	"testing"

	"legos-factory/factory/internal/fact"
)

func TestRetryOtherModelUsesFallbacks(t *testing.T) {
	s := newSim(t)
	s.lim.BuilderFallbacks = 1
	s.building(pt("a", nil, "a/"))
	s.must(DispatchBuild([]string{"a"}))
	s.emit(fact.KindTaskFailed, fact.TaskFailed{Task: "a", Class: "task"})
	s.must(RetryTask("a", ChangeOtherModel))
	if tk, _ := s.state().Task("a"); tk.ModelChanges != 1 || tk.Status != TaskPending {
		t.Fatalf("task = %+v", tk)
	}
	s.must(DispatchBuild([]string{"a"}))
	s.emit(fact.KindTaskFailed, fact.TaskFailed{Task: "a", Class: "task"})
	err := s.call(RetryTask("a", ChangeOtherModel))
	if err == nil || !strings.Contains(err.Error(), "no fallback model left") {
		t.Fatalf("err = %v", err)
	}
	s.must(RetryTask("a", ChangeSame))
}

func TestSplitReplacesTask(t *testing.T) {
	s := newSim(t)
	s.building(pt("pre", nil, "pre/"), pt("a", []string{"pre"}, "a/"), pt("post", []string{"a"}, "post/"))
	s.must(DispatchBuild([]string{"pre"}))
	s.built("pre")
	s.must(RequestInspection("pre"))
	s.inspected("pre", 0)
	s.must(DispatchBuild([]string{"a"}))
	s.emit(fact.KindTaskFailed, fact.TaskFailed{Task: "a", Class: "task"})
	s.must(RetryTask("a", ChangeSplit))
	if tk, _ := s.state().Task("a"); !tk.SplitPending {
		t.Fatal("split not pending")
	}
	subs := []PlanTask{pt("a1", nil, "a/x/"), pt("a2", nil, "a/y/")}
	if err := s.state().ValidateSplit("a", subs); err != nil {
		t.Fatal(err)
	}
	s.emit(KindPlanSplit, PlanSplit{Task: "a", Tasks: subs})
	st := s.state()
	var ids []string
	for _, tk := range st.Tasks {
		ids = append(ids, tk.ID)
	}
	if !reflect.DeepEqual(ids, []string{"pre", "a1", "a2", "post"}) {
		t.Fatalf("tasks = %v", ids)
	}
	a1, _ := st.Task("a1")
	post, _ := st.Task("post")
	if a1.Status != TaskPending || !reflect.DeepEqual(a1.DependsOn, []string{"pre"}) || !reflect.DeepEqual(post.DependsOn, []string{"a1", "a2"}) {
		t.Fatalf("a1 = %+v post = %+v", a1, post)
	}
}

func TestValidateSplit(t *testing.T) {
	s := newSim(t)
	s.building(pt("a", nil, "a/"), pt("b", nil, "b/"))
	cases := []struct {
		name string
		subs []PlanTask
		want string
	}{
		{"ok", []PlanTask{pt("a1", nil, "a/x/"), pt("a2", nil, "a/y/")}, ""},
		{"one task", []PlanTask{pt("a1", nil, "a/")}, "at least 2"},
		{"outside parent", []PlanTask{pt("a1", nil, "a/x/"), pt("a2", nil, "b/")}, "not within"},
		{"parent widened", []PlanTask{pt("a1", nil, "a/x/"), pt("a2", nil, ".")}, "not within"},
		{"id clash", []PlanTask{pt("a1", nil, "a/x/"), pt("b", nil, "a/y/")}, "already exists"},
		{"overlapping parallel", []PlanTask{pt("a1", nil, "a/x/"), pt("a2", nil, "a/x/y/")}, "overlapping"},
		{"overlap resolved by dependency", []PlanTask{pt("a1", nil, "a/x/"), pt("a2", []string{"a1"}, "a/x/y/")}, ""},
	}
	for _, tc := range cases {
		err := s.state().ValidateSplit("a", tc.subs)
		if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestWithin(t *testing.T) {
	for _, tc := range []struct {
		sub     string
		parents []string
		want    bool
	}{
		{"a/x", []string{"a/"}, true}, {"a", []string{"a/"}, true}, {"ab/x", []string{"a/"}, false},
		{"a/", []string{"a/x/"}, false}, {"a/x/*.go", []string{"a/**"}, true}, {"z", []string{"."}, true},
	} {
		if got := Within(tc.sub, tc.parents); got != tc.want {
			t.Errorf("Within(%q, %v) = %v", tc.sub, tc.parents, got)
		}
	}
}
