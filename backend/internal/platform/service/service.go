// Package service is the shared composition-root runner: config, logging,
// health server, signal handling, graceful shutdown (doc 13 §rules, doc 23 §7).
// Each cmd/ main wires its dependencies inside run().
package service

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/freezxp/netinv/backend/internal/platform/config"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
	"github.com/freezxp/netinv/backend/internal/platform/logx"
)

// Runtime is handed to each service's run function.
type Runtime struct {
	Cfg    config.Common
	Log    *slog.Logger
	Health *httpx.HealthServer
}

// RunFunc contains the service's real work. It must respect ctx cancellation
// and return promptly on shutdown. Returning nil keeps the process alive
// until a signal arrives (skeleton services do this).
type RunFunc func(ctx context.Context, rt *Runtime) error

// Run executes the standard service lifecycle and exits the process.
func Run(name string, fn RunFunc) {
	cfg, err := config.Load(name)
	if err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(2)
	}
	log := logx.New(cfg.Service, cfg.LogLevel, cfg.LogPretty)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	rt := &Runtime{Cfg: cfg, Log: log, Health: httpx.NewHealthServer(cfg.HTTPAddr, log)}

	healthErr := make(chan error, 1)
	go func() { healthErr <- rt.Health.Start(ctx) }()

	log.Info("service starting", "log_level", cfg.LogLevel)
	runErr := make(chan error, 1)
	go func() { runErr <- fn(ctx, rt) }()

	var exitErr error
	select {
	case exitErr = <-runErr:
		stop()
	case exitErr = <-healthErr:
		stop()
	case <-ctx.Done():
	}
	if exitErr != nil && !errors.Is(exitErr, context.Canceled) {
		log.Error("service failed", "err", exitErr)
		os.Exit(1)
	}
	log.Info("service stopped")
}
