package reports

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"io"
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

func renderPDF(t *testing.T, rep *Report, brand settings.Branding, charts ...Chart) []byte {
	t.Helper()
	w := httptest.NewRecorder()
	if err := writePDF(w, rep, brand, charts); err != nil {
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

// pdfText inflates the page content streams so a test can assert on what the
// document actually says.
//
// fpdf compresses them, which matters more than it sounds: a test that greps
// the raw bytes for a string can never find one, so it passes whether or not
// the text is correct. The regression guard below — that no UTF-8 pair reaches
// the page untranslated — was exactly that shape before this helper existed.
func pdfText(t *testing.T, doc []byte) string {
	t.Helper()
	var out bytes.Buffer
	rest := doc
	for {
		i := bytes.Index(rest, []byte("stream"))
		if i < 0 {
			break
		}
		body := rest[i+len("stream"):]
		body = bytes.TrimLeft(body, "\r\n")
		j := bytes.Index(body, []byte("endstream"))
		if j < 0 {
			break
		}
		if zr, err := zlib.NewReader(bytes.NewReader(body[:j])); err == nil {
			if raw, err := io.ReadAll(zr); err == nil {
				out.Write(raw)
			}
			zr.Close()
		}
		rest = body[j:]
	}
	if out.Len() == 0 {
		t.Fatal("no page content could be read back from the PDF")
	}
	return out.String()
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
	text := pdfText(t, renderPDF(t, rep, settings.Branding{Company: "Ærø Netværk"}))
	// cp1252 encodes these as single bytes; the UTF-8 pairs must not survive
	// into the content stream.
	for _, bad := range []string{"Ã¦", "Ã¸", "Ã¶", "â"} {
		if strings.Contains(text, bad) {
			t.Fatalf("UTF-8 bytes %q reached the PDF untranslated", bad)
		}
	}
	// And the translated forms did arrive: an assertion that only rules out
	// mojibake also passes on a page with no text at all.
	for _, want := range []string{"Netv", "Kraftwerk K", "Bandwidth report"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q on the page", want)
		}
	}
}

// The default table names an interface by its alias alone. The device name and
// the ifName are internal topology; a page that goes to a customer should not
// carry which chassis their circuit lands on.
func TestDefaultColumnsDropDeviceAndInterfaceName(t *testing.T) {
	text := pdfText(t, renderPDF(t, sampleReport(false), settings.Branding{}))
	if !strings.Contains(text, "ACME-WAN-01") {
		t.Error("the alias is the identity the table prints; it is missing")
	}
	for _, gone := range []string{"core-sw-1", "ge-0/0/1", "Device", "Interface"} {
		if strings.Contains(text, gone) {
			t.Errorf("%q is on the page, but the default columns exclude it", gone)
		}
	}
}

// An operator who wants the device back gets it back, in the order they chose.
func TestConfiguredColumnsAreHonoured(t *testing.T) {
	text := pdfText(t, renderPDF(t, sampleReport(false), settings.Branding{
		Columns: []string{"device", "interface", "alias", "total_in"},
	}))
	for _, want := range []string{"core-sw-1", "ge-0/0/1", "ACME-WAN-01", "Total in"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q on the page", want)
		}
	}
	// And what was not chosen is not there: a column list is a whitelist, not
	// an addition to the default.
	if strings.Contains(text, "Util p95") {
		t.Error("an unchosen column was printed")
	}
}

// Whatever the chosen subset, the table fills the page. A configuration that
// resolves to nothing falls back rather than printing a report with no columns.
func TestColumnsAlwaysFillTheWidthAndNeverVanish(t *testing.T) {
	for _, b := range []settings.Branding{
		{},
		{Columns: []string{"alias"}},
		{Columns: []string{"alias", "avg_in", "avg_out"}},
		{Columns: []string{"nonsense-key-that-was-removed"}},
	} {
		cols := columnsFor(&Report{}, b)
		if len(cols) == 0 {
			t.Fatalf("columns for %v resolved to an empty table", b.Columns)
		}
		total := 0.0
		for _, c := range cols {
			total += c.width
		}
		if diff := total - availW; diff > 0.01 || diff < -0.01 {
			t.Errorf("columns for %v total %.2fmm, want %.2fmm", b.Columns, total, availW)
		}
	}
}

// Charts are drawn per row and are what the export gains over the CSV.
func TestChartsAreDrawnAndCarryTheirLabels(t *testing.T) {
	rep := sampleReport(false)
	charts := []Chart{
		{Title: "ACME-WAN-01", Subtitle: "Acme Ltd", In: ramp(60, 4e6), Out: ramp(60, 1e6)},
		{Title: "unlabelled-port"},
	}
	text := pdfText(t, renderPDF(t, rep, settings.Branding{}, charts...))
	for _, want := range []string{"Traffic detail", "ACME-WAN-01", "In", "Out",
		"No traffic data in this period"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q on the page", want)
		}
	}
}

// A capped chart set says so. A reader who counts ten charts under a forty-row
// table must be told the rest were dropped by a limit rather than by having no
// traffic.
func TestACappedChartSetIsDisclosed(t *testing.T) {
	rep := sampleReport(false)
	text := pdfText(t, renderPDF(t, rep, settings.Branding{},
		Chart{Title: "only-one", In: ramp(20, 1e6)}))
	if !strings.Contains(text, "further rows appear in the table only") {
		t.Error("the page does not say that charts were capped")
	}
}

// The preview must render for a deployment that has collected nothing, and it
// must say on its face that the numbers are invented.
func TestPreviewRendersAndAdmitsItIsASample(t *testing.T) {
	brand := settings.Branding{Company: "Acme Networks"}
	rep, charts := SamplePDF(brand)
	text := pdfText(t, renderPDF(t, rep, brand, charts...))
	if !strings.Contains(text, "Sample data") {
		t.Error("a preview that does not say it is a preview is a document someone forwards")
	}
	if !strings.Contains(text, "Traffic detail") {
		t.Error("the preview should show the charts it is previewing")
	}
	if _, off := SamplePDF(settings.Branding{Charts: "none"}); off != nil {
		t.Error("charts:none must preview without charts")
	}
}

// ramp spreads n samples across the sample report's window, so the chart under
// test is actually drawn across the plot rather than collapsed onto its left
// edge by the clamp.
func ramp(n int, peak float64) []Point {
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).Unix()
	to := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC).Unix()
	pts := make([]Point, n)
	for i := range pts {
		pts[i] = Point{
			T: from + (to-from)*int64(i)/int64(n-1),
			V: peak * float64(i%10) / 10,
		}
	}
	return pts
}
