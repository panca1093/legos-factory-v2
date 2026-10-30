package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"legos-factory/factory/internal/engine"
	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/harness"
	"legos-factory/factory/internal/sandbox"
	"legos-factory/factory/internal/scm"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "factory:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: factory create|run|watch|status [flags]")
	}
	cmd := args[0]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	db := fs.String("db", "factory.db", "fact store path")
	rt := fs.String("runtime", "local", "sandbox runtime: local|docker")
	root := fs.String("root", ".factory-work", "workspace root (local runtime)")
	image := fs.String("image", "legos-factory-sandbox", "sandbox image (docker runtime)")
	projectPath := fs.String("project", "", "project JSON file")
	objective := fs.String("objective", "", "work order objective (create)")
	wo := fs.String("wo", "", "work order id")
	interval := fs.Duration("interval", 30*time.Second, "poll interval (watch)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	store, err := fact.Open(*db)
	if err != nil {
		return err
	}
	defer store.Close()

	var runtime sandbox.Runtime
	switch *rt {
	case "local":
		abs, err := filepath.Abs(*root)
		if err != nil {
			return err
		}
		runtime = sandbox.Local{Root: abs}
	case "docker":
		runtime = sandbox.Docker{Image: *image}
	default:
		return fmt.Errorf("unknown runtime %q", *rt)
	}
	e := &engine.Engine{
		Facts:   store,
		Runtime: runtime,
		Harness: harness.NewACP(),
		SCM: func(p engine.Project) (scm.Provider, error) {
			return scm.New(p.SCMKind, p.SCMBaseURL, p.SCMUsername, p.SCMToken, &http.Client{Timeout: 30 * time.Second})
		},
		Log: slog.Default(),
	}

	if cmd == "status" {
		return printStatus(ctx, e, *wo)
	}
	if *projectPath == "" {
		return errors.New("-project is required")
	}
	p, err := engine.LoadProject(*projectPath)
	if err != nil {
		return err
	}
	switch cmd {
	case "create":
		if *objective == "" {
			return errors.New("-objective is required")
		}
		id, err := e.Create(ctx, p, *objective)
		if err == nil {
			fmt.Println(id)
		}
		return err
	case "run":
		if *wo == "" {
			return errors.New("-wo is required")
		}
		if err := e.Run(ctx, p, *wo); err != nil {
			return err
		}
		return printStatus(ctx, e, *wo)
	case "watch":
		if *wo == "" {
			return errors.New("-wo is required")
		}
		return watch(ctx, e, p, *wo, *interval)
	}
	return fmt.Errorf("unknown command %q", cmd)
}

func printStatus(ctx context.Context, e *engine.Engine, wo string) error {
	if wo == "" {
		return errors.New("-wo is required")
	}
	st, stalled, err := e.Status(ctx, wo)
	if err != nil {
		return err
	}
	fmt.Printf("%s %s", wo, st)
	if stalled {
		fmt.Print(" (stalled)")
	}
	fmt.Println()
	return nil
}

func watch(ctx context.Context, e *engine.Engine, p engine.Project, wo string, interval time.Duration) error {
	var last fact.Status
	for {
		for _, s := range []struct {
			name string
			fn   func(context.Context, engine.Project, string) error
		}{{"run", e.Run}, {"observe", e.Observe}} {
			name := s.name
			if err := s.fn(ctx, p, wo); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if !engine.IsTransient(err) {
					return fmt.Errorf("%s: %w", name, err)
				}
				e.Log.Warn("transient failure, will retry", "step", name, "err", err)
			}
		}
		st, _, err := e.Status(ctx, wo)
		if err != nil {
			return err
		}
		if st != last {
			fmt.Printf("%s %s\n", wo, st)
			last = st
		}
		if st.Terminal() || st == fact.StatusCIGreen {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
