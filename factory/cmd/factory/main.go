package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"legos-factory/factory/internal/api"
	"legos-factory/factory/internal/engine"
	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/harness"
	"legos-factory/factory/internal/profile"
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
		return errors.New("usage: factory create|run|watch|status|serve [flags]")
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
	addr := fs.String("addr", ":8090", "listen address (serve)")
	tokenEnv := fs.String("token-env", "FACTORY_OWNER_TOKEN", "env var holding the owner bearer token (serve)")
	keyEnv := fs.String("key-env", "FACTORY_SECRET_KEY", "env var holding the 32-byte credential key, hex or base64 (serve)")
	projectsDir := fs.String("projects-dir", "projects", "project JSON directory (serve)")
	plansFile := fs.String("plans", "plans.json", "subscription plans file (serve)")
	singleOwner := fs.Bool("single-owner", true, "allow subscription credentials (serve)")
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
		runtime = sandbox.Local{Root: *root}
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

	if cmd == "serve" {
		return serve(ctx, e, *db, *addr, *tokenEnv, *keyEnv, *projectsDir, *plansFile, *singleOwner)
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

func decodeKey(s string) ([]byte, error) {
	if b, err := hex.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	return nil, errors.New("secret key must be 32 bytes, hex or base64 encoded")
}

func serve(ctx context.Context, e *engine.Engine, db, addr, tokenEnv, keyEnv, projectsDir, plansFile string, singleOwner bool) error {
	token := os.Getenv(tokenEnv)
	if token == "" {
		return fmt.Errorf("env %s (owner token) is empty", tokenEnv)
	}
	key, err := decodeKey(os.Getenv(keyEnv))
	if err != nil {
		return fmt.Errorf("env %s: %w", keyEnv, err)
	}
	ps, err := profile.Open(profile.Config{DB: db, Key: key, SingleOwner: singleOwner, PlansFile: plansFile})
	if err != nil {
		return err
	}
	defer ps.Close()
	e.Profiles = ps
	h := &api.Server{Token: token, Profiles: ps, Engine: e, ProjectsDir: projectsDir, BaseCtx: ctx, Log: e.Log}
	srv := &http.Server{Addr: addr, Handler: h.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	e.Log.Info("serving", "addr", addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
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
