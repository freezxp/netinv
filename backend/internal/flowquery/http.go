package flowquery

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/freezxp/netinv/backend/internal/flow"
	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
)

// Handler — /flow (doc 09 §7.1). Gated on metrics:read: these are the same
// series the metrics proxy already exposes, shaped so a client does not have
// to know the traps in reading them.
type Handler struct {
	Svc     *Service
	Checker authz.Checker
}

func (h *Handler) Register(r chi.Router) {
	r.Group(func(g chi.Router) {
		g.Use(httpx.RequirePerm(h.Checker, authz.MetricsRead))
		g.Get("/flow/top", h.top)
		g.Get("/flow/series", h.series)
		g.Get("/flow/exporters", h.exporters)
		// Per-device conveniences: the same queries with the device taken
		// from the path, which is where an integration holding a device id
		// expects to find them.
		g.Get("/devices/{id}/flow/top", h.top)
		g.Get("/devices/{id}/flow/series", h.series)
	})
}

// defaultWindow is the last hour: long enough that a UniFi gateway's 5-minute
// export bursts (doc 34 §4.2) do not read as silence, short enough to be cheap.
const defaultWindow = time.Hour

func (h *Handler) window(r *http.Request) (time.Time, time.Time, error) {
	q := r.URL.Query()
	to, err := parseAt(q.Get("to"), h.Svc.now().UTC())
	if err != nil {
		return time.Time{}, time.Time{}, errx.New(errx.KindInvalid, "bad 'to': %s", err)
	}
	from, err := parseAt(q.Get("from"), to.Add(-defaultWindow))
	if err != nil {
		return time.Time{}, time.Time{}, errx.New(errx.KindInvalid, "bad 'from': %s", err)
	}
	return from, to, nil
}

func (h *Handler) filter(r *http.Request) (Filter, error) {
	q := r.URL.Query()
	from, to, err := h.window(r)
	if err != nil {
		return Filter{}, err
	}
	f := Filter{
		Dimension: flow.Dimension(q.Get("dimension")),
		From:      from, To: to,
		DeviceID: q.Get("device"),
		SiteID:   q.Get("site"),
		IfIndex:  q.Get("if_index"),
	}
	if id := chi.URLParam(r, "id"); id != "" {
		f.DeviceID = id
	}
	if f.Dimension == "" {
		f.Dimension = flow.DimTalker
	}
	if f.IfIndex != "" {
		if _, err := strconv.ParseUint(f.IfIndex, 10, 32); err != nil {
			return Filter{}, errx.New(errx.KindInvalid, "if_index must be a non-negative integer")
		}
	}
	for _, e := range q["exporter"] {
		for _, part := range strings.Split(e, ",") {
			if part = strings.TrimSpace(part); part != "" {
				f.Exporters = append(f.Exporters, part)
			}
		}
	}
	return f, nil
}

func intParam(r *http.Request, name string, def, max int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > max {
		return 0, errx.New(errx.KindInvalid, "%s must be between 1 and %d", name, max)
	}
	return n, nil
}

func (h *Handler) top(w http.ResponseWriter, r *http.Request) {
	f, err := h.filter(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	limit, err := intParam(r, "limit", DefaultLimit, MaxLimit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.Svc.Top(r.Context(), f, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (h *Handler) series(w http.ResponseWriter, r *http.Request) {
	f, err := h.filter(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	limit, err := intParam(r, "limit", DefaultSeries, MaxSeries)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var step time.Duration
	if raw := r.URL.Query().Get("step"); raw != "" {
		step, err = parseStep(raw)
		if err != nil {
			httpx.WriteError(w, r, errx.New(errx.KindInvalid, "bad 'step': %s", err))
			return
		}
	}
	res, err := h.Svc.Series(r.Context(), f, limit, step)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (h *Handler) exporters(w http.ResponseWriter, r *http.Request) {
	from, to, err := h.window(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.Svc.Exporters(r.Context(), from, to)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func parseAt(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return def, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("want RFC3339 or a unix timestamp")
	}
	return time.Unix(n, 0).UTC(), nil
}

// parseStep takes a Go duration ("5m") or bare seconds ("300"), the two forms
// Prometheus clients send.
func parseStep(s string) (time.Duration, error) {
	if n, err := strconv.Atoi(s); err == nil {
		if n <= 0 {
			return 0, fmt.Errorf("must be positive")
		}
		return time.Duration(n) * time.Second, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("want seconds or a duration like 5m")
	}
	return d, nil
}
