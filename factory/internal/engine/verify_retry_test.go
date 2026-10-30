package engine

import (
	"context"
	"strings"
	"testing"

	"legos-factory/factory/internal/fact"
)

func TestRetryAfterVerifyFailureRebuildsWithVerifyOutput(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t, `if [ -f fixed.txt ]; then exit 0; fi; echo "FAIL needs fixed.txt"; exit 1`)
	ev.h.edit = func(call int, dir string) error {
		if call == 1 {
			return writeFile("out.txt", "built\n")(call, dir)
		}
		return writeFile("fixed.txt", "ok\n")(call, dir)
	}
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if c := failedClass(t, ev.e, id); c != fact.ClassVerify {
		t.Fatalf("class = %q", c)
	}
	if err := ev.e.Retry(ctx, id, "try again"); err != nil {
		t.Fatal(err)
	}
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, id))
	}
	if len(ev.h.prompts) != 2 || !strings.Contains(ev.h.prompts[1], "FAIL needs fixed.txt") {
		t.Errorf("builder prompts = %q", ev.h.prompts)
	}
}
