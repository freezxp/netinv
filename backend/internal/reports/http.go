package reports

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/freezxp/netinv/backend/internal/inventory/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
	"github.com/freezxp/netinv/backend/internal/platform/settings"
)

type Handler struct {
	Svc     *Service
	Checker authz.Checker
	// Branding is optional: a deployment that has never configured it still
	// exports, unbranded, rather than failing.
	Branding BrandingReader
}

// BrandingReader is the settings side, kept as an interface so reports does
// not depend on the settings package's storage.
type BrandingReader interface {
	GetBranding(ctx context.Context) (settings.Branding, error)
}

func (h *Handler) Register(r chi.Router) {
	r.Group(func(pr chi.Router) {
		pr.Use(httpx.RequirePerm(h.Checker, authz.DevicesRead))
		pr.Get("/reports/bandwidth", h.bandwidth)
	})
}

// bandwidth serves the interface traffic report as JSON, CSV or PDF.
//
// The exports are not an afterthought: a bandwidth report's destination is
// usually a spreadsheet, an invoice or a capacity meeting, and a report you
// cannot get out of the tool is a report someone retypes. CSV is for the
// spreadsheet, where the numbers get recalculated; PDF is for the person, who
// reads them as they are and needs the page to say who produced it and over
// what window.
func (h *Handler) bandwidth(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	to, err := parseAt(q.Get("to"), time.Now().UTC())
	if err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "bad 'to': %s", err))
		return
	}
	from, err := parseAt(q.Get("from"), to.Add(-24*time.Hour))
	if err != nil {
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "bad 'from': %s", err))
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	filter := postgres.InterfaceFilter{Q: q.Get("q"), Customer: q.Get("customer")}
	var rep *Report
	if q.Get("group") == "customer" {
		rep, err = h.Svc.ByCustomer(r.Context(), filter, from, to, limit)
	} else {
		rep, err = h.Svc.Bandwidth(r.Context(), filter, from, to, limit)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	switch q.Get("format") {
	case "csv":
		writeCSV(w, rep)
	case "pdf":
		// Branding failing to load must not lose the report: an export
		// without a logo is still the numbers someone asked for.
		var brand settings.Branding
		if h.Branding != nil {
			if b, berr := h.Branding.GetBranding(r.Context()); berr == nil {
				brand = b
			}
		}
		if err := writePDF(w, rep, brand); err != nil {
			httpx.WriteError(w, r, errx.Wrap(errx.KindInternal, err, "render pdf"))
		}
	default:
		httpx.WriteJSON(w, http.StatusOK, rep)
	}
}

func writeCSV(w http.ResponseWriter, rep *Report) {
	name := fmt.Sprintf("netinv-bandwidth-%s_%s.csv",
		rep.From.UTC().Format("20060102T1504"), rep.To.UTC().Format("20060102T1504"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	c := csv.NewWriter(w)
	// The window is in the filename and in a header row: a CSV that has been
	// emailed twice has to say which period it covers, and a spreadsheet keeps
	// the rows long after the filename is forgotten.
	_ = c.Write([]string{"# window", rep.From.UTC().Format(time.RFC3339),
		rep.To.UTC().Format(time.RFC3339), "match", rep.Query, "customer", rep.Customer})
	if rep.GroupedBy == "customer" {
		writeGroupedCSV(c, rep)
		c.Flush()
		return
	}
	_ = c.Write([]string{
		"customer", "device", "interface", "alias", "description", "speed_bps",
		"avg_in_bps", "avg_out_bps", "p95_in_bps", "p95_out_bps",
		"max_in_bps", "max_out_bps", "total_in_bytes", "total_out_bytes",
		"avg_util_pct", "p95_util_pct", "max_util_pct",
	})
	for _, row := range rep.Rows {
		_ = c.Write([]string{
			row.Customer, row.DeviceName, row.Name, row.Alias, row.Descr,
			strconv.FormatInt(row.SpeedBPS, 10),
			f(row.AvgInBPS), f(row.AvgOutBPS), f(row.P95InBPS), f(row.P95OutBPS),
			f(row.MaxInBPS), f(row.MaxOutBPS), f(row.TotalInBytes), f(row.TotalOutBytes),
			pct(row.AvgUtilPct), pct(row.P95UtilPct), pct(row.MaxUtilPct),
		})
	}
	c.Flush()
}

// A grouped export has different columns, not blank ones: a spreadsheet full
// of empty device cells invites someone to fill them in.
func writeGroupedCSV(c *csv.Writer, rep *Report) {
	_ = c.Write([]string{
		"customer", "interfaces", "speed_bps",
		"avg_in_bps", "avg_out_bps", "p95_in_bps", "p95_out_bps",
		"max_in_bps", "max_out_bps", "total_in_bytes", "total_out_bytes",
		"avg_util_pct", "p95_util_pct", "max_util_pct",
	})
	for _, row := range rep.Rows {
		name := row.Customer
		if name == "" {
			name = "(untagged)"
		}
		_ = c.Write([]string{
			name, strconv.Itoa(row.Interfaces), strconv.FormatInt(row.SpeedBPS, 10),
			f(row.AvgInBPS), f(row.AvgOutBPS), f(row.P95InBPS), f(row.P95OutBPS),
			f(row.MaxInBPS), f(row.MaxOutBPS), f(row.TotalInBytes), f(row.TotalOutBytes),
			pct(row.AvgUtilPct), pct(row.P95UtilPct), pct(row.MaxUtilPct),
		})
	}
}

func f(v float64) string { return strconv.FormatFloat(v, 'f', 0, 64) }

// pct leaves the cell empty when the speed is unknown. Writing 0 would be read
// as an idle interface by every spreadsheet that opens this.
func pct(v float64) string {
	if v < 0 {
		return ""
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// parseAt accepts RFC3339 or a unix timestamp, matching the metrics proxy.
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
