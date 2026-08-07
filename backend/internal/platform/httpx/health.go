// Package httpx hosts shared HTTP plumbing. This file: the operational
// endpoints every service exposes — /healthz, /readyz, /metrics (NFR-52).
package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

// HealthServer serves liveness/readiness for one service. Readiness starts
// false and is flipped by the composition root once dependencies are wired.
type HealthServer struct {
	srv   *http.Server
	mux   *http.ServeMux
	ready atomic.Bool
	log   *slog.Logger
}

func NewHealthServer(addr string, log *slog.Logger) *HealthServer {
	h := &HealthServer{log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if h.ready.Load() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ready"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready"))
	})
	h.mux = mux
	h.srv = &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return h
}

// Handle mounts an application handler on the service's HTTP server (the API
// serves /api/v1/ alongside its operational endpoints).
func (h *HealthServer) Handle(pattern string, handler http.Handler) {
	h.mux.Handle(pattern, handler)
}

func (h *HealthServer) SetReady(ready bool) { h.ready.Store(ready) }

// Start serves until the context is cancelled, then shuts down gracefully.
func (h *HealthServer) Start(ctx context.Context) error {
	errc := make(chan error, 1)
	go func() { errc <- h.srv.ListenAndServe() }()
	h.log.Info("health server listening", "addr", h.srv.Addr)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return h.srv.Shutdown(shutCtx)
	}
}
