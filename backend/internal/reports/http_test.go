package reports

import (
	"context"
	"encoding/csv"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/freezxp/netinv/backend/internal/inventory/adapters/postgres"
	"github.com/freezxp/netinv/backend/internal/platform/settings"
)

// allowAll stands in for the permission checker: the routes' authorization is
// exercised where the middleware lives, and mixing it in here would only test
// chi's ability to chain handlers.
type allowAll struct{}

func (allowAll) Has([]string, string) bool { return true }

type stubBranding struct{ b settings.Branding }

func (s stubBranding) GetBranding(_ context.Context) (settings.Branding, error) {
	return s.b, nil
}

func handler(brand settings.Branding) chi.Router {
	rows := []postgres.InterfaceSearchRow{
		{DeviceID: "d1", DeviceName: "core-sw-1", IfIndex: 3, Name: "ge-0/0/1",
			Alias: "ACME-WAN-01", Customer: "Acme Ltd", SpeedBPS: 1_000_000_000},
		{DeviceID: "d1", DeviceName: "core-sw-1", IfIndex: 4, Name: "ge-0/0/2"},
	}
	svc, _ := svc(rows, map[string][]Sample{
		"avg_over_time": {sample("d1", "3", "in", 12_500_000)},
	})
	r := chi.NewRouter()
	(&Handler{Svc: svc, Checker: allowAll{}, Branding: stubBranding{brand}}).Register(r)
	return r
}

// The three formats are one endpoint, and each has a destination that makes it
// wrong to serve one when another was asked for.
func TestBandwidthServesJSONCSVAndPDF(t *testing.T) {
	h := handler(settings.Branding{Company: "Acme Networks"})
	for _, c := range []struct{ query, ctype, prefix string }{
		{"", "application/json", "{"},
		{"&format=csv", "text/csv; charset=utf-8", "# window"},
		{"&format=pdf", "application/pdf", "%PDF-"},
		{"&format=csv&group=customer", "text/csv; charset=utf-8", "# window"},
		{"&format=pdf&group=customer", "application/pdf", "%PDF-"},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/reports/bandwidth?q=acme"+c.query, nil))
		if w.Code != 200 {
			t.Errorf("%q: status %d — %s", c.query, w.Code, w.Body.String())
			continue
		}
		if ct := w.Header().Get("Content-Type"); ct != c.ctype {
			t.Errorf("%q: content-type %q, want %q", c.query, ct, c.ctype)
		}
		if !strings.HasPrefix(w.Body.String(), c.prefix) {
			t.Errorf("%q: body starts %q, want %q", c.query,
				w.Body.String()[:min(12, w.Body.Len())], c.prefix)
		}
	}
}

// The CSV keeps the device and interface name whatever the PDF's columns are
// set to: it goes to a spreadsheet, where the identity is what a lookup joins
// on, not to a customer.
func TestCSVIsUnaffectedByTheReportColumns(t *testing.T) {
	h := handler(settings.Branding{Columns: []string{"alias"}})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/reports/bandwidth?format=csv", nil))
	rd := csv.NewReader(strings.NewReader(w.Body.String()))
	// The window preamble is a short row: this file is deliberately not a
	// uniform table, so the reader must not insist that it is.
	rd.FieldsPerRecord = -1
	rows, err := rd.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	header := strings.Join(rows[1], ",")
	for _, want := range []string{"device", "interface", "alias", "customer"} {
		if !strings.Contains(header, want) {
			t.Errorf("CSV header %q is missing %q", header, want)
		}
	}
}

// A window that ends before it starts is a mistake worth an error rather than
// an empty report, which reads as "nothing happened".
func TestBandwidthRejectsABadWindow(t *testing.T) {
	h := handler(settings.Branding{})
	for _, q := range []string{
		"from=2026-08-02T00:00:00Z&to=2026-08-01T00:00:00Z",
		"from=yesterday",
		"to=whenever",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/reports/bandwidth?"+q, nil))
		if w.Code < 400 {
			t.Errorf("%q: status %d, want a rejection", q, w.Code)
		}
	}
}

// The preview renders from the body, so an operator sees an unsaved logo or
// column choice before committing to it.
func TestPreviewRendersFromTheRequestBody(t *testing.T) {
	h := handler(settings.Branding{})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/reports/preview",
		strings.NewReader(`{"company":"Unsaved Co","charts":"none"}`)))
	if w.Code != 200 {
		t.Fatalf("status %d — %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("content-type = %q", ct)
	}
	if !strings.Contains(pdfText(t, w.Body.Bytes()), "Unsaved Co") {
		t.Error("the preview did not use the branding from the body")
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/reports/preview", strings.NewReader("not json")))
	if w.Code < 400 {
		t.Errorf("malformed body accepted: %d", w.Code)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
