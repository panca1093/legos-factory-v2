package usecase

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"legos-factory/backend/internal/model"
)

func TestWriteFileAtomicConcurrentWritersNeverExposeAPartialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner.cjs")
	data := bytes.Repeat([]byte("x"), 1<<20)
	stop := make(chan struct{})
	var readers, writers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if b, err := os.ReadFile(path); err == nil && len(b) != len(data) {
				t.Errorf("partial file of %d bytes", len(b))
				return
			}
		}
	}()
	for i := 0; i < 8; i++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for j := 0; j < 20; j++ {
				if err := writeFileAtomic(path, data, 0o644); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	writers.Wait()
	close(stop)
	readers.Wait()
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestSmokeNoAffectedPagesIsOptionalEvenWhenBlocking(t *testing.T) {
	r := newSmokeRig(t, nil, smokeTree)
	p := smokeProfile()
	p.Smoke.Blocking = true
	c, res := r.f.runSmoke(context.Background(), r.wo, p)
	if res.Outcome != model.Pass || !c.Optional {
		t.Fatalf("a pass that opened no page must not count as required evidence: %+v optional=%v", res, c.Optional)
	}
}
