package httpapi

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/freezxp/netinv/backend/internal/audit"
	"github.com/freezxp/netinv/backend/internal/inventory/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/inventory/app"
	"github.com/freezxp/netinv/backend/internal/inventory/domain"
	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
)

type DeviceHandler struct {
	Svc     *app.DeviceService
	Checker authz.Checker
	// DispatchSync publishes an on-demand sync job; wired in cmd (nil = 503).
	DispatchSync func(ctx context.Context, d *domain.Device) (jobID string, err error)
}

// Register adds /devices routes (doc 09 §6) to an authenticated router.
func (h *DeviceHandler) Register(r chi.Router) {
	r.Group(func(pr chi.Router) {
		pr.Use(httpx.RequirePerm(h.Checker, authz.DevicesRead))
		pr.Get("/devices", h.list)
		pr.Get("/devices/{id}", h.get)
		pr.Get("/devices/{id}/interfaces", h.interfaces)
		// Fleet-wide, not device-scoped: the search exists for when you do
		// not know which device holds the port.
		pr.Get("/interfaces", h.searchInterfaces)
		pr.Get("/interfaces/customers", h.customers)
		pr.Get("/devices/{id}/history", h.history)
		pr.Get("/devices/{id}/sync-runs", h.syncRuns)
		pr.Get("/devices/{id}/neighbors", h.neighbors)
	})
	r.Group(func(pw chi.Router) {
		pw.Use(httpx.RequirePerm(h.Checker, authz.DevicesWrite))
		pw.Post("/devices", h.create)
		pw.Patch("/devices/{id}", h.update)
		pw.Post("/devices/import", h.importCSV)
		pw.Post("/devices/{id}/retire", h.status(domain.DeviceRetired))
		pw.Post("/devices/{id}/enable", h.status(domain.DeviceActive))
		pw.Post("/devices/{id}/disable", h.status(domain.DeviceDisabled))
		pw.Post("/devices/{id}/sync", h.syncNow)
		// Tagging is operator knowledge about the network, so it is a write
		// even though it changes nothing the network can see.
		pw.Post("/interfaces/tags", h.tagInterfaces)
		// Live SNMP walk: read-only, but it loads the device, so operator+.
		pw.Get("/devices/{id}/oids", h.oids)
	})
	// Permanent deletion is Admin-only (doc 20 §5: devices:admin).
	r.Group(func(pa chi.Router) {
		pa.Use(httpx.RequirePerm(h.Checker, authz.DevicesAdmin))
		pa.Delete("/devices/{id}", h.purge)
	})
}

type deviceView struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	MgmtIP       string   `json:"mgmt_ip"`
	SiteID       string   `json:"site_id"`
	ConnectorID  string   `json:"connector_id"`
	CredentialID string   `json:"credential_id"`
	ProfileID    string   `json:"profile_id"`
	Status       string   `json:"status"`
	SysName      string   `json:"sys_name,omitempty"`
	Vendor       string   `json:"vendor,omitempty"`
	Model        string   `json:"model,omitempty"`
	SerialNumber string   `json:"serial_number,omitempty"`
	OSVersion    string   `json:"os_version,omitempty"`
	Tags         []string `json:"tags"`
	Notes        string   `json:"notes,omitempty"`
	// Subscribed uplink rate in bits/s; 0 when nobody has stated it.
	WANCapacityBPS int64 `json:"wan_capacity_bps"`
	// Extra source addresses this device exports flow from, beyond mgmt_ip.
	FlowExporters []string `json:"flow_exporters,omitempty"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}

func toDeviceView(d *domain.Device) deviceView {
	const rfc = "2006-01-02T15:04:05Z"
	return deviceView{
		ID: d.ID, Name: d.Name, MgmtIP: d.MgmtIP, SiteID: d.SiteID,
		ConnectorID: d.ConnectorID, CredentialID: d.CredentialID,
		ProfileID: d.ProfileID, Status: string(d.Status), SysName: d.SysName,
		Vendor: d.Vendor, Model: d.Model, SerialNumber: d.SerialNumber,
		OSVersion: d.OSVersion, Tags: d.Tags, Notes: d.Notes,
		WANCapacityBPS: d.WANCapacityBPS,
		FlowExporters:  flowExporters(d.Attrs),
		CreatedAt:      d.CreatedAt.UTC().Format(rfc), UpdatedAt: d.UpdatedAt.UTC().Format(rfc),
	}
}

// flowExporters reads the attribute back out of jsonb, where a []string went in
// and a []any comes out.
func flowExporters(attrs map[string]any) []string {
	raw, ok := attrs["flow_exporters"]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, a := range v {
			if s, ok := a.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func (h *DeviceHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := app.DeviceFilter{
		Query:  q.Get("q"),
		Cursor: q.Get("cursor"),
	}
	// Minimal filter grammar subset (FR-DEV-04): site:eq:X,status:in:a|b
	for part := range strings.SplitSeq(q.Get("filter"), ",") {
		bits := strings.SplitN(part, ":", 3)
		if len(bits) != 3 {
			continue
		}
		switch bits[0] + ":" + bits[1] {
		case "site:eq":
			f.SiteID = bits[2]
		case "status:in":
			f.Status = strings.Split(bits[2], "|")
		case "status:eq":
			f.Status = []string{bits[2]}
		}
	}
	items, next, err := h.Svc.Repo.List(r.Context(), f)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]deviceView, 0, len(items))
	for _, d := range items {
		out = append(out, toDeviceView(d))
	}
	var cursor any
	if next != "" {
		cursor = next
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out, "next_cursor": cursor})
}

func (h *DeviceHandler) get(w http.ResponseWriter, r *http.Request) {
	d, err := h.Svc.Repo.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toDeviceView(d))
}

func (h *DeviceHandler) create(w http.ResponseWriter, r *http.Request) {
	var in app.DeviceInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "malformed JSON body"))
		return
	}
	d, err := h.Svc.Create(r.Context(), in, h.meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/devices/"+d.ID)
	httpx.WriteJSON(w, http.StatusCreated, toDeviceView(d))
}

func (h *DeviceHandler) update(w http.ResponseWriter, r *http.Request) {
	var in app.DeviceInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "malformed JSON body"))
		return
	}
	d, err := h.Svc.Update(r.Context(), chi.URLParam(r, "id"), in, h.meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toDeviceView(d))
}

func (h *DeviceHandler) status(target domain.DeviceStatus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h.Svc.SetStatus(r.Context(), chi.URLParam(r, "id"), target, h.meta(r)); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		d, err := h.Svc.Repo.Get(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, toDeviceView(d))
	}
}

func (h *DeviceHandler) interfaces(w http.ResponseWriter, r *http.Request) {
	repo := h.Svc.Repo.(*postgres.DeviceRepo)
	rows, err := repo.Interfaces(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": rows})
}

func (h *DeviceHandler) history(w http.ResponseWriter, r *http.Request) {
	repo := h.Svc.Repo.(*postgres.DeviceRepo)
	rows, err := repo.History(r.Context(), chi.URLParam(r, "id"), 100)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": rows})
}

// syncRuns exposes platform.sync_runs (doc 09 §6). The failure reason for a
// device that never leaves 'pending' is recorded there and, before this
// endpoint, was readable only with psql — which meant an operator watching a
// device fail had no way to learn why from the product.
func (h *DeviceHandler) syncRuns(w http.ResponseWriter, r *http.Request) {
	repo := h.Svc.Repo.(*postgres.DeviceRepo)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := repo.SyncRuns(r.Context(), chi.URLParam(r, "id"), limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// The site's collection health rides along rather than sitting behind its
	// own endpoint. "Why is this device pending" has two halves — a run that
	// failed, or no run at all because nothing polls this site — and the page
	// cannot name the cause holding only one of them. Two round-trips to answer
	// one question is the worse shape.
	site, err := repo.SiteCollection(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": rows, "site": site})
}

// searchInterfaces serves the Interfaces page (doc 30 §9): find a port by what
// an operator wrote on it — alias or description — across every device.
func (h *DeviceHandler) searchInterfaces(w http.ResponseWriter, r *http.Request) {
	repo := h.Svc.Repo.(*postgres.DeviceRepo)
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	rows, total, err := repo.FindInterfaces(r.Context(), postgres.InterfaceFilter{
		Q:        strings.TrimSpace(q.Get("q")),
		Customer: strings.TrimSpace(q.Get("customer")),
		UpOnly:   q.Get("up") == "1",
		Sort:     q.Get("sort"),
		Desc:     q.Get("dir") == "desc",
	}, limit, offset)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": rows, "total": total})
}

// customers lists assigned customer names and their interface counts, for the
// filter. Derived from the interfaces themselves — there is no customer entity
// in NetInv, and inventing one would put an onboarding step in front of
// tagging a single port.
func (h *DeviceHandler) customers(w http.ResponseWriter, r *http.Request) {
	repo := h.Svc.Repo.(*postgres.DeviceRepo)
	rows, err := repo.Customers(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": rows})
}

// tagInterfaces assigns customers and tags in bulk, from CSV or JSON.
//
// CSV because the list already exists somewhere else — a billing system, a
// spreadsheet, an old NMS — and retyping it into a UI one port at a time is
// the work worth removing. Columns: device, interface, customer, tags.
func (h *DeviceHandler) tagInterfaces(w http.ResponseWriter, r *http.Request) {
	repo := h.Svc.Repo.(*postgres.DeviceRepo)
	var in []postgres.TagAssignment
	var err error
	if strings.HasPrefix(r.Header.Get("Content-Type"), "text/csv") {
		in, err = parseTagCSV(r.Body)
	} else {
		var body struct {
			Assignments []struct {
				Device    string    `json:"device"`
				Interface string    `json:"interface"`
				Customer  string    `json:"customer"`
				Tags      *[]string `json:"tags"`
			} `json:"assignments"`
		}
		if derr := json.NewDecoder(r.Body).Decode(&body); derr != nil {
			err = errx.New(errx.KindInvalid, "malformed JSON body")
		} else {
			for _, a := range body.Assignments {
				t := postgres.TagAssignment{Device: a.Device, Interface: a.Interface,
					Customer: a.Customer}
				if a.Tags != nil {
					t.Tags, t.SetTags = *a.Tags, true
				}
				in = append(in, t)
			}
		}
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if len(in) == 0 {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "no assignments to apply"))
		return
	}
	res, err := repo.ApplyTags(r.Context(), in)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.Svc.Audit.Write(r.Context(), audit.Event{
		ActorKind: "user", ActorID: httpx.ClaimsFrom(r.Context()).Subject,
		Action: "interface.tag", ResourceKind: "interface",
		Detail: map[string]any{"submitted": len(in), "updated": res.Updated,
			"unmatched": len(res.Unmatched), "ambiguous": len(res.Ambiguous)},
	})
	httpx.WriteJSON(w, http.StatusOK, res)
}

// parseTagCSV reads the import format. A header row is required and its column
// names are honoured, because an export from somewhere else will not have the
// columns in the order this happens to expect, and silently reading customer
// names out of the wrong column is the worst outcome available.
func parseTagCSV(rd io.Reader) ([]postgres.TagAssignment, error) {
	c := csv.NewReader(rd)
	c.FieldsPerRecord = -1
	c.TrimLeadingSpace = true
	records, err := c.ReadAll()
	if err != nil {
		return nil, errx.New(errx.KindInvalid, "malformed CSV: %s", err)
	}
	if len(records) < 2 {
		return nil, errx.New(errx.KindInvalid,
			"expected a header row (device,interface,customer,tags) and at least one row")
	}
	col := map[string]int{}
	for i, name := range records[0] {
		col[strings.ToLower(strings.TrimSpace(name))] = i
	}
	if _, ok := col["device"]; !ok {
		return nil, errx.New(errx.KindInvalid, "CSV has no 'device' column")
	}
	if _, ok := col["interface"]; !ok {
		return nil, errx.New(errx.KindInvalid, "CSV has no 'interface' column")
	}
	at := func(rec []string, name string) string {
		i, ok := col[name]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}
	var out []postgres.TagAssignment
	for _, rec := range records[1:] {
		if len(rec) == 0 || strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue // blank line
		}
		a := postgres.TagAssignment{
			Device: at(rec, "device"), Interface: at(rec, "interface"),
			Customer: at(rec, "customer"),
		}
		if _, ok := col["tags"]; ok {
			raw := at(rec, "tags")
			a.SetTags = true
			for _, t := range strings.Split(raw, "|") {
				if t = strings.TrimSpace(t); t != "" {
					a.Tags = append(a.Tags, t)
				}
			}
			if a.Tags == nil {
				a.Tags = []string{}
			}
		}
		out = append(out, a)
	}
	return out, nil
}

func (h *DeviceHandler) neighbors(w http.ResponseWriter, r *http.Request) {
	repo := h.Svc.Repo.(*postgres.DeviceRepo)
	rows, err := repo.Neighbors(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": rows})
}

// oids dumps what the device actually exposes over SNMP (doc 30 §5) — the
// tool for discovering which MIBs a platform supports.
func (h *DeviceHandler) oids(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 1000
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		limit = n
	}
	values, err := h.Svc.WalkOIDs(r.Context(), chi.URLParam(r, "id"),
		q.Get("root"), limit, h.meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"data": values, "truncated": len(values) >= limit,
	})
}

// purge permanently deletes a retired device (FR-DEV-08).
func (h *DeviceHandler) purge(w http.ResponseWriter, r *http.Request) {
	if err := h.Svc.Purge(r.Context(), chi.URLParam(r, "id"), h.meta(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *DeviceHandler) syncNow(w http.ResponseWriter, r *http.Request) {
	if h.DispatchSync == nil {
		httpx.WriteError(w, r, errx.New(errx.KindTransient, "sync dispatch unavailable"))
		return
	}
	d, err := h.Svc.Repo.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	jobID, err := h.DispatchSync(r.Context(), d)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]string{"job_id": jobID})
}

func (h *DeviceHandler) importCSV(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "multipart form with a 'file' field is required"))
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "missing 'file' field"))
		return
	}
	defer file.Close()
	results, err := h.Svc.ImportCSV(r.Context(), file, h.meta(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	created := 0
	for _, res := range results {
		if res.Error == "" {
			created++
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"rows": len(results), "created": created, "results": results,
	})
}

func (h *DeviceHandler) meta(r *http.Request) app.Meta {
	ip := r.RemoteAddr
	if i := strings.LastIndex(ip, ":"); i > 0 {
		ip = ip[:i]
	}
	return app.Meta{
		Actor:     httpx.ClaimsFrom(r.Context()).Subject,
		SourceIP:  ip,
		UserAgent: r.UserAgent(),
		TraceID:   httpx.TraceID(r.Context()),
	}
}
