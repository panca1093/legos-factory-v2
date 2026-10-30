package fact

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestAppendBatchIgnoresListedKindsInConflictCheck(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first, _ := s.Append(ctx, "wo-1", "a", nil)
	if _, err := s.Append(ctx, "wo-1", "run.step", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendBatch(ctx, "wo-1", first.ID, []NewFact{{"b", nil}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want conflict without the ignore list", err)
	}
	if _, err := s.Append(ctx, "wo-1", "c", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendBatch(ctx, "wo-1", first.ID, []NewFact{{"d", nil}}, "run.step"); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want conflict from the non-ignorable fact", err)
	}
	last, _ := s.Append(ctx, "wo-1", "run.step", nil)
	if _, err := s.AppendBatch(ctx, "wo-1", last.ID-1, []NewFact{{"e", nil}}, "run.step", "run.started"); err != nil {
		t.Fatalf("err = %v, want run.step ignored", err)
	}
}
