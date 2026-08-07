// Package logx provides the structured logger used by every service (doc 21).
// JSON to stdout with the canonical fields; pretty text handler for local dev.
package logx

import (
	"log/slog"
	"os"
	"strings"
)

// Version is stamped at build time via -ldflags.
var Version = "dev"

// New returns a logger carrying the canonical svc/ver fields (doc 21 §1).
func New(service, level string, pretty bool) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}
	var h slog.Handler
	if pretty {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h).With("svc", "netinv-"+service, "ver", Version)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
