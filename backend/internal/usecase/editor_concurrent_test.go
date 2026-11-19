package usecase

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestEditorConcurrentSavesWithTheSameBaseHashConflict(t *testing.T) {
	r := newEditorRig(t)
	ctx := context.Background()
	f := r.open(t)
	put(t, r.project, "a.txt", []byte("v1"))
	fc, err := r.ed.Read(ctx, r.ana, f.ID, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = r.ed.Write(ctx, r.ana, f.ID, "a.txt", fmt.Sprintf("w%d", i), fc.Hash, false)
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		var wc *WriteConflict
		switch {
		case err == nil:
			ok++
		case !errors.As(err, &wc):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d saves passed, want exactly 1", ok)
	}
}
