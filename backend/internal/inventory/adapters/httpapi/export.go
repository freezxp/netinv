// Inventory export (FR-EXP-01): streams the full filtered result set as CSV
// or XLSX. v1 serves synchronously — at design-ceiling row counts this moves
// behind the async job queue per doc 09 §13.
package httpapi

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/xuri/excelize/v2"

	"github.com/freezxp/netinv/backend/internal/audit"
	"github.com/freezxp/netinv/backend/internal/inventory/app"
	"github.com/freezxp/netinv/backend/internal/inventory/domain"
	"github.com/freezxp/netinv/backend/internal/platform/authz"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/httpx"
)

type ExportHandler struct {
	Repo    app.DeviceRepo
	Audit   audit.Writer
	Checker authz.Checker
}

func (h *ExportHandler) Register(r chi.Router) {
	r.Group(func(g chi.Router) {
		g.Use(httpx.RequirePerm(h.Checker, authz.ExportsRun))
		g.Get("/exports/inventory", h.inventory)
	})
}

var exportHeader = []string{"name", "mgmt_ip", "status", "site_id", "vendor",
	"model", "serial_number", "os_version", "sys_name", "tags", "created_at"}

func deviceRow(d *domain.Device) []string {
	return []string{d.Name, d.MgmtIP, string(d.Status), d.SiteID, d.Vendor,
		d.Model, d.SerialNumber, d.OSVersion, d.SysName,
		strings.Join(d.Tags, ";"), d.CreatedAt.UTC().Format(time.RFC3339)}
}

// collect walks the keyset cursor to export every matching row, not just one
// page (FR-EXP-01), with a sanity cap.
func (h *ExportHandler) collect(r *http.Request) ([]*domain.Device, error) {
	f := app.DeviceFilter{Query: r.URL.Query().Get("q"), Limit: 200}
	for part := range strings.SplitSeq(r.URL.Query().Get("filter"), ",") {
		bits := strings.SplitN(part, ":", 3)
		if len(bits) != 3 {
			continue
		}
		switch bits[0] + ":" + bits[1] {
		case "site:eq":
			f.SiteID = bits[2]
		case "status:eq":
			f.Status = []string{bits[2]}
		case "status:in":
			f.Status = strings.Split(bits[2], "|")
		}
	}
	var all []*domain.Device
	for {
		page, next, err := h.Repo.List(r.Context(), f)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if next == "" || len(all) >= 100_000 {
			return all, nil
		}
		f.Cursor = next
	}
}

func (h *ExportHandler) inventory(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "csv"
	}
	devices, err := h.collect(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	claims := httpx.ClaimsFrom(r.Context())
	h.Audit.Write(r.Context(), audit.Event{
		ActorKind: "user", ActorID: claims.Subject, Action: "export.inventory",
		Detail:  map[string]any{"format": format, "rows": len(devices)},
		TraceID: httpx.TraceID(r.Context()),
	})
	stamp := time.Now().UTC().Format("20060102-150405")

	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition",
			fmt.Sprintf(`attachment; filename="netinv-inventory-%s.csv"`, stamp))
		cw := csv.NewWriter(w)
		_ = cw.Write(exportHeader)
		for _, d := range devices {
			_ = cw.Write(deviceRow(d))
		}
		cw.Flush()
	case "xlsx":
		f := excelize.NewFile()
		sheet := "Inventory"
		_ = f.SetSheetName("Sheet1", sheet)
		_ = f.SetSheetRow(sheet, "A1", &exportHeader)
		for i, d := range devices {
			row := deviceRow(d)
			cells := make([]any, len(row))
			for j, v := range row {
				cells[j] = v
			}
			_ = f.SetSheetRow(sheet, fmt.Sprintf("A%d", i+2), &cells)
		}
		// Metadata sheet per FR-EXP-01.
		meta := "Export info"
		_, _ = f.NewSheet(meta)
		_ = f.SetSheetRow(meta, "A1", &[]any{"exported_at", time.Now().UTC().Format(time.RFC3339)})
		_ = f.SetSheetRow(meta, "A2", &[]any{"exported_by", claims.Username})
		_ = f.SetSheetRow(meta, "A3", &[]any{"filter", r.URL.Query().Get("filter")})
		_ = f.SetSheetRow(meta, "A4", &[]any{"rows", len(devices)})
		w.Header().Set("Content-Type",
			"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		w.Header().Set("Content-Disposition",
			fmt.Sprintf(`attachment; filename="netinv-inventory-%s.xlsx"`, stamp))
		_ = f.Write(w)
	default:
		httpx.WriteError(w, r, errx.New(errx.KindInvalid, "format must be csv or xlsx"))
	}
}
