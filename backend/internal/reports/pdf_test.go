package reports

import (
	"bytes"
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/freezxp/netinv/backend/internal/platform/settings"
)

func sampleReport(grouped bool) *Report {
	rep := &Report{
		From: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
		Rows: []Row{{
			DeviceName: "core-sw-1", Name: "ge-0/0/1", Alias: "ACME-WAN-01",
			Customer: "Acme Ltd", SpeedBPS: 1_000_000_000,
			AvgInBPS: 12_500_000, P95InBPS: 88_000_000, MaxInBPS: 210_000_000,
			TotalInBytes: 4.2e12, P95UtilPct: 8.8, Interfaces: 3,
		}, {
			// The untagged group, and a port with no speed: both print as a
			// dash rather than as a blank or a zero.
			DeviceName: "core-sw-2", Name: "ge-0/0/2", P95UtilPct: -1,
		}},
	}
	if grouped {
		rep.GroupedBy = "customer"
	}
	return rep
}

func renderPDF(t *testing.T, rep *Report, brand settings.Branding) []byte {
	t.Helper()
	w := httptest.NewRecorder()
	if err := writePDF(w, rep, brand); err != nil {
		t.Fatalf("render: %v", err)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("content-type = %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, ".pdf") {
		t.Fatalf("content-disposition = %q", cd)
	}
	b := w.Body.Bytes()
	if !bytes.HasPrefix(b, []byte("%PDF-")) {
		t.Fatal("output is not a PDF")
	}
	return b
}

// The export must render on a deployment that has never configured branding.
// Reports are the feature; branding is decoration on top of it.
func TestPDFRendersUnbranded(t *testing.T) {
	b := renderPDF(t, sampleReport(false), settings.Branding{})
	if len(b) < 800 {
		t.Fatalf("suspiciously small PDF: %d bytes", len(b))
	}
}

func TestPDFRendersGroupedAndEmptyReports(t *testing.T) {
	renderPDF(t, sampleReport(true), settings.Branding{Company: "Acme Networks"})
	empty := sampleReport(false)
	empty.Rows = nil
	// A report matching nothing must still produce a page saying so, not a
	// zero-byte download that looks like a broken button.
	renderPDF(t, empty, settings.Branding{})
}

// A malformed logo must not cost the operator their report.
func TestPDFSurvivesAnUnusableLogo(t *testing.T) {
	for _, logo := range []string{
		"data:image/png;base64,not-base64-at-all",
		"data:text/html;base64,PGgxPmhpPC9oMT4=",
		"https://example.com/logo.png",
		"",
	} {
		renderPDF(t, sampleReport(false), settings.Branding{
			Company: "Acme Networks", Logo: logo,
		})
	}
}

// A real logo is embedded, which is the whole point of the feature.
func TestPDFEmbedsALogo(t *testing.T) {
	// The smallest valid PNG: one transparent pixel.
	px, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	brand := settings.Branding{
		Company: "Acme Networks", Subtitle: "Network Operations",
		Footer: "Commercial in confidence", Accent: "#1d4ed8",
		Logo: "data:image/png;base64," + base64.StdEncoding.EncodeToString(px),
	}
	withLogo := renderPDF(t, sampleReport(false), brand)
	brand.Logo = ""
	without := renderPDF(t, sampleReport(false), brand)
	if len(withLogo) <= len(without) {
		t.Fatalf("logo added no bytes: %d vs %d", len(withLogo), len(without))
	}
}

// Numbers are for reading, so they are scaled and unit-suffixed rather than
// printed as raw bits per second.
func TestBpsReadsLikeAPersonWouldWriteIt(t *testing.T) {
	for _, c := range []struct {
		in   float64
		want string
	}{
		{0, "—"}, {-1, "—"}, {512, "512 bps"}, {1500, "1.5 kbps"},
		{12_500_000, "12.5 Mbps"}, {210_000_000, "210 Mbps"},
		{4_000_000_000, "4.0 Gbps"},
	} {
		if got := bps(c.in); got != c.want {
			t.Errorf("bps(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Unknown utilization is a dash. Printing 0% would read as an idle circuit,
// when in fact ifSpeed was missing.
func TestUnknownUtilizationIsNotZero(t *testing.T) {
	if got := pctCell(-1); got != "—" {
		t.Fatalf("pctCell(-1) = %q, want a dash", got)
	}
	if got := pctCell(0); got != "0.0%" {
		t.Fatalf("pctCell(0) = %q, want a real zero", got)
	}
}

// Every string reaching the page goes through the code-page translator. The
// period line is the one a reader checks first, and it carries an em-dash;
// untranslated it renders as mojibake on an otherwise correct report.
func TestPDFTranslatesNonASCIIText(t *testing.T) {
	rep := sampleReport(false)
	rep.Rows[0].Customer = "Kraftwerk Köln"
	b := renderPDF(t, rep, settings.Branding{Company: "Ærø Netværk"})
	// cp1252 encodes these as single bytes; the UTF-8 pairs must not survive
	// into the content stream.
	for _, bad := range [][]byte{[]byte("Ã¦"), []byte("Ã¸"), []byte("Ã¶"), []byte("â")} {
		if bytes.Contains(b, bad) {
			t.Fatalf("UTF-8 bytes %q reached the PDF untranslated", bad)
		}
	}
}
