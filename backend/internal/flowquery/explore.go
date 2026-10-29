package flowquery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/freezxp/netinv/backend/internal/flowstore"
	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
)

// Explorer serves the raw flow store (ADR-026, doc 09 §7.2): the Flows page's
// top-N and time series over any field, individual flows, and the store's
// status. It shares the address book with the aggregate endpoints, so a
// device means the same set of exporter addresses in both.
type Explorer struct {
	CH      *flowstore.Client
	Book    AddressBook
	Ifaces  InterfaceNamer
	Checker authz.Checker
	// Retention mirrors the collector's settings; the raw cutoff is derived
	// from it so a query reaching past raw data reads the rollup instead.
	Retention flowstore.Retention
	Now       func() time.Time
}

// InterfaceNamer resolves (device, ifIndex) to an interface name.
type InterfaceNamer interface {
	InterfaceNames(ctx context.Context, deviceIDs []string) (map[string]map[uint32]string, error)
}

func (e *Explorer) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Register mounts the explorer. With no ClickHouse configured, only status
// answers, saying so — the page then explains how to turn raw storage on
// instead of showing errors.
func (e *Explorer) Register(r chi.Router) {
	r.Group(func(g chi.Router) {
		g.Use(httpx.RequirePerm(e.Checker, authz.MetricsRead))
		g.Get("/flow/explore/status", e.status)
		g.Post("/flow/explore", e.explore)
		g.Post("/flow/records", e.records)
	})
}

// exploreRequest is the explorer's body. Device and site are filters like any
// other in the UI, but they resolve to exporter addresses here, which is why
// they travel separately from the store's own filters.
type exploreRequest struct {
	From    string             `json:"from"`
	To      string             `json:"to"`
	Filters []flowstore.Filter `json:"filters"`
	GroupBy []string           `json:"group_by"`
	Metric  string             `json:"metric"`
	Limit   int                `json:"limit"`
	OrderBy string             `json:"order_by"`
	Devices []string           `json:"devices"`
	Sites   []string           `json:"sites"`
}

// Labels names the exporters and interfaces appearing in a response.
type Labels struct {
	// Exporters maps an exporter address to its device.
	Exporters map[string]ExporterLabel `json:"exporters"`
	// Interfaces maps "address|ifIndex" to the interface name.
	Interfaces map[string]string `json:"interfaces"`
}

// ExporterLabel is the device an exporter address belongs to.
type ExporterLabel struct {
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
}

func (e *Explorer) status(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"enabled":     e.CH != nil,
		"raw_days":    e.Retention.RawDays,
		"rollup_days": e.Retention.RollupDays,
		"fields":      flowstore.Fields,
	}
	if e.CH != nil {
		st, err := flowstore.ReadStatus(r.Context(), e.CH)
		if err != nil {
			// Configured but unreachable, or the collector has not created
			// the schema yet: still a 200, because the page has to say which.
			out["error"] = err.Error()
		} else {
			out["store"] = st
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (e *Explorer) parse(r *http.Request) (*exploreRequest, *flowstore.Query, map[string]Attribution, error) {
	if e.CH == nil {
		return nil, nil, nil, errx.New(errx.KindNotFound, "raw flow storage is not configured (NETINV_FLOW_CH_URL)")
	}
	var in exploreRequest
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(&in); err != nil {
		return nil, nil, nil, errx.New(errx.KindInvalid, "body must be a JSON explorer query")
	}
	now := e.now().UTC()
	to, err := parseAt(in.To, now)
	if err != nil {
		return nil, nil, nil, errx.New(errx.KindInvalid, "bad 'to': %s", err)
	}
	from, err := parseAt(in.From, to.Add(-time.Hour))
	if err != nil {
		return nil, nil, nil, errx.New(errx.KindInvalid, "bad 'from': %s", err)
	}
	if max := time.Duration(e.Retention.RollupDays) * 24 * time.Hour; max > 0 && to.Sub(from) > max {
		return nil, nil, nil, errx.New(errx.KindInvalid, "the window is longer than the %d-day rollup retention", e.Retention.RollupDays)
	}
	q := &flowstore.Query{
		From: from, To: to, Filters: in.Filters, GroupBy: in.GroupBy,
		Metric: in.Metric, Limit: in.Limit,
	}
	if e.Retention.RawDays > 0 {
		q.RawCutoff = now.Add(-time.Duration(e.Retention.RawDays) * 24 * time.Hour)
	}

	list, err := e.Book.Addresses(r.Context())
	if err != nil {
		return nil, nil, nil, err
	}
	byAddr := map[string]Attribution{}
	for _, a := range list {
		k := normAddr(a.Addr)
		if _, seen := byAddr[k]; !seen {
			byAddr[k] = a
		}
	}
	if len(in.Devices) > 0 || len(in.Sites) > 0 {
		want := map[string]bool{}
		for _, d := range in.Devices {
			want["d:"+d] = true
		}
		for _, s := range in.Sites {
			want["s:"+s] = true
		}
		q.Exporters = []netip.Addr{}
		for k, a := range byAddr {
			if want["d:"+a.DeviceID] || want["s:"+a.SiteID] {
				if addr, err := netip.ParseAddr(k); err == nil {
					q.Exporters = append(q.Exporters, addr)
				}
			}
		}
	}
	return &in, q, byAddr, nil
}

func (e *Explorer) explore(w http.ResponseWriter, r *http.Request) {
	in, q, book, err := e.parse(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := flowstore.Explore(r.Context(), e.CH, q)
	if err != nil {
		httpx.WriteError(w, r, storeErr(err))
		return
	}
	// Name what the rows show: exporter keys by device, and interface keys
	// ("address|ifIndex") by interface name on that device.
	labels := Labels{Exporters: map[string]ExporterLabel{}, Interfaces: map[string]string{}}
	devices := map[string]bool{}
	note := func(addr string) {
		if _, done := labels.Exporters[addr]; done {
			return
		}
		if att, ok := book[normAddr(addr)]; ok {
			labels.Exporters[addr] = ExporterLabel{DeviceID: att.DeviceID, DeviceName: att.DeviceName}
			devices[att.DeviceID] = true
		}
	}
	var ifKeys []string
	for _, g := range res.Groups {
		for i, n := range in.GroupBy {
			switch n {
			case "exporter":
				note(g.Keys[i])
			case "in_if", "out_if":
				if addr, _, ok := strings.Cut(g.Keys[i], "|"); ok {
					note(addr)
					ifKeys = append(ifKeys, g.Keys[i])
				}
			}
		}
	}
	if len(ifKeys) > 0 && e.Ifaces != nil {
		ids := make([]string, 0, len(devices))
		for d := range devices {
			ids = append(ids, d)
		}
		if names, err := e.Ifaces.InterfaceNames(r.Context(), ids); err == nil {
			for _, k := range ifKeys {
				addr, idx, _ := strings.Cut(k, "|")
				n, err := strconv.ParseUint(idx, 10, 32)
				if err != nil {
					continue
				}
				if name, ok := names[labels.Exporters[addr].DeviceID][uint32(n)]; ok {
					labels.Interfaces[k] = name
				}
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"result": res, "labels": labels})
}

func (e *Explorer) records(w http.ResponseWriter, r *http.Request) {
	in, q, book, err := e.parse(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rows, err := flowstore.Records(r.Context(), e.CH, &flowstore.RecordsQuery{Query: *q, OrderBy: in.OrderBy})
	if err != nil {
		httpx.WriteError(w, r, storeErr(err))
		return
	}
	labels := Labels{Exporters: map[string]ExporterLabel{}, Interfaces: map[string]string{}}
	devices := map[string]bool{}
	for _, row := range rows {
		a, _ := row["exporter"].(string)
		if _, done := labels.Exporters[a]; done || a == "" {
			continue
		}
		if att, ok := book[normAddr(a)]; ok {
			labels.Exporters[a] = ExporterLabel{DeviceID: att.DeviceID, DeviceName: att.DeviceName}
			devices[att.DeviceID] = true
		}
	}
	if len(devices) > 0 && e.Ifaces != nil {
		ids := make([]string, 0, len(devices))
		for d := range devices {
			ids = append(ids, d)
		}
		if names, err := e.Ifaces.InterfaceNames(r.Context(), ids); err == nil {
			for _, row := range rows {
				a, _ := row["exporter"].(string)
				dev := labels.Exporters[a].DeviceID
				for _, col := range []string{"in_if", "out_if"} {
					idx := uint32(parseUintAny(row[col])) //nolint:gosec // an ifIndex column is UInt32
					if n, ok := names[dev][idx]; ok {
						labels.Interfaces[a+"|"+strconv.FormatUint(uint64(idx), 10)] = n
					}
				}
			}
		}
	}
	if rows == nil {
		rows = []map[string]any{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"records": rows, "labels": labels})
}

// storeErr shows a request the store refused as the user's mistake, and
// anything else as the store being unavailable — never as a 500 carrying
// ClickHouse's internals.
func storeErr(err error) error {
	var qe *flowstore.QueryError
	if errors.As(err, &qe) {
		return errx.New(errx.KindInvalid, "%s", qe.Msg)
	}
	var ce *flowstore.Error
	if errors.As(err, &ce) && strings.Contains(ce.Body, "UNKNOWN_TABLE") {
		return errx.New(errx.KindTransient, "raw flow storage has no tables yet — the collector creates them when it first reaches ClickHouse")
	}
	return errx.Wrap(errx.KindTransient, err, "flow store query failed")
}

// parseUintAny reads a number from a decoded ClickHouse row.
func parseUintAny(v any) uint64 {
	switch n := v.(type) {
	case json.Number:
		u, _ := strconv.ParseUint(n.String(), 10, 64)
		return u
	case float64:
		if n >= 0 {
			return uint64(n)
		}
	}
	return 0
}
