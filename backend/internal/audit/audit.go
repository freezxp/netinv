// Package audit — append-only audit event writing (FR-AUD, doc 08 audit.events).
// Writes never fail the business operation; failures are logged and counted.
package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Event struct {
	TenantID     string
	ActorKind    string // user | api_token | system
	ActorID      string
	Action       string // dot-namespaced, e.g. auth.login.success
	ResourceKind string
	ResourceID   string
	Before       any
	After        any
	SourceIP     string
	UserAgent    string
	TraceID      string
	Detail       map[string]any
}

// Writer records audit events. Fake implementations are used in unit tests.
type Writer interface {
	Write(ctx context.Context, e Event)
}

type PGWriter struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
}

func (w *PGWriter) Write(ctx context.Context, e Event) {
	if e.TenantID == "" {
		e.TenantID = "t_default"
	}
	if e.Detail == nil {
		e.Detail = map[string]any{}
	}
	// Detached context: an aborted request must still leave its audit trace.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err := w.Pool.Exec(wctx, `
		INSERT INTO audit.events
		  (tenant_id, actor_kind, actor_id, action, resource_kind, resource_id,
		   before, after, source_ip, user_agent, trace_id, detail)
		VALUES ($1,$2,nullif($3,''),$4,nullif($5,''),nullif($6,''),$7,$8,
		        nullif($9,'')::inet,nullif($10,''),nullif($11,''),$12)`,
		e.TenantID, e.ActorKind, e.ActorID, e.Action, e.ResourceKind, e.ResourceID,
		e.Before, e.After, e.SourceIP, e.UserAgent, e.TraceID, e.Detail)
	if err != nil {
		w.Log.Error("audit write failed", "action", e.Action, "err", err)
	}
}
