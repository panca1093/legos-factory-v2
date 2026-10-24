package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"legos-factory/backend/internal/app"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := app.LoadConfig()
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}

	a, err := app.Build(cfg, logger)
	if err != nil {
		logger.Error("build", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := a.Start(ctx); err != nil {
		logger.Error("start", "err", err)
		os.Exit(1)
	}

	go func() {
		<-ctx.Done()
		shctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = a.Server.Echo.Shutdown(shctx)
	}()

	logger.Info("factory listening", "addr", cfg.Addr, "data_dir", cfg.DataDir, "auth_mode", cfg.AuthMode)
	if err := a.Server.Echo.Start(cfg.Addr); err != nil {
		logger.Info("server stopped", "err", err)
	}

	// final cleanup
	a.Sched.Stop()
	if a.DBCloser != nil {
		_ = a.DBCloser()
	}
}
