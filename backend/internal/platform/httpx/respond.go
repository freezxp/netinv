package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

// Error envelope per doc 23 §5 / FR-API-04.
type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details any    `json:"details,omitempty"`
		TraceID string `json:"trace_id"`
	} `json:"error"`
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError maps the errx taxonomy to HTTP statuses (doc 23 §1). 5xx bodies
// never leak internals; details live in logs keyed by trace_id.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	kind := errx.KindOf(err)
	status, code := http.StatusInternalServerError, "internal"
	msg := "internal error"
	switch kind {
	case errx.KindInvalid:
		status, code, msg = http.StatusUnprocessableEntity, "validation_failed", err.Error()
	case errx.KindUnauthorized:
		status, code, msg = http.StatusUnauthorized, "unauthorized", "authentication required or invalid"
	case errx.KindForbidden:
		status, code, msg = http.StatusForbidden, "forbidden", err.Error()
	case errx.KindNotFound:
		status, code, msg = http.StatusNotFound, "not_found", "resource not found"
	case errx.KindConflict:
		status, code, msg = http.StatusConflict, "conflict", err.Error()
	case errx.KindTransient:
		status, code, msg = http.StatusServiceUnavailable, "unavailable", "temporarily unavailable"
	}
	var body apiError
	body.Error.Code = code
	body.Error.Message = msg
	body.Error.TraceID = TraceID(r.Context())
	WriteJSON(w, status, body)
}

type ctxKey int

const traceKey ctxKey = 1

// TraceMiddleware assigns a trace_id to every request (doc 21 §3).
func TraceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 8)
		_, _ = rand.Read(buf)
		tid := hex.EncodeToString(buf)
		w.Header().Set("X-Trace-Id", tid)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), traceKey, tid)))
	})
}

func TraceID(ctx context.Context) string {
	if v, ok := ctx.Value(traceKey).(string); ok {
		return v
	}
	return ""
}
