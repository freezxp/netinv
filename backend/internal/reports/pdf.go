package reports

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"

	"github.com/freezxp/netinv/backend/internal/platform/settings"
)

// PDF export.
//
// CSV goes to a spreadsheet; this goes to a person. A capacity meeting, a
// monthly pack sent to a customer, an attachment on a contract review — all
// places where the numbers are read rather than recalculated, and where a page
// that does not say who produced it, over what period, is a page that gets
// queried. So the branding is not decoration: the company name, the window and
// the generation time are what make an exported page answerable later.
//
// Rendered server-side rather than in the browser for two reasons: the output
// is identical whoever exports it, and the same renderer is available to
// anything that later wants to attach a report to an email.

// Landscape A4 in millimetres, less the margins used below.
const (
	pdfMarginX  = 10.0
	pdfTopY     = 10.0
	pdfPageW    = 297.0
	pdfPageH    = 210.0
	pdfBottomY  = pdfPageH - 14.0
	pdfLogoMaxH = 14.0
	pdfLogoMaxW = 40.0
)

type pdfCol struct {
	title string
	width float64
	right bool // numeric columns are right-aligned so digits line up
}

// interfaceCols and customerCols mirror the two CSV shapes. A grouped export
// gets different columns rather than blank ones, for the same reason: a table
// full of empty device cells invites someone to fill them in.
var interfaceCols = []pdfCol{
	{"Customer", 34, false}, {"Device", 32, false}, {"Interface", 24, false},
	{"Alias", 46, false}, {"Speed", 20, true},
	{"Avg in", 21, true}, {"Avg out", 21, true},
	{"P95 in", 21, true}, {"P95 out", 21, true},
	{"Peak in", 21, true}, {"Peak out", 21, true},
	{"Util p95", 21, true},
}

var customerCols = []pdfCol{
	{"Customer", 56, false}, {"Circuits", 20, true}, {"Speed", 24, true},
	{"Avg in", 26, true}, {"Avg out", 26, true},
	{"P95 in", 26, true}, {"P95 out", 26, true},
	{"Peak in", 26, true}, {"Peak out", 26, true},
	{"Total in", 24, true},
}

func writePDF(w http.ResponseWriter, rep *Report, brand settings.Branding) error {
	cols := interfaceCols
	if rep.GroupedBy == "customer" {
		cols = customerCols
	}
	r, g, b := brand.AccentRGB()

	doc := fpdf.New("L", "mm", "A4", "")
	doc.SetMargins(pdfMarginX, pdfTopY, pdfMarginX)
	// Manual page breaks: the table header has to be redrawn on each page, and
	// fpdf's automatic break would start a page mid-table without it, leaving
	// columns of numbers with nothing saying what they are.
	doc.SetAutoPageBreak(false, 0)
	doc.SetTitle("NetInv bandwidth report", true)
	doc.SetCreator(brand.CompanyOrDefault(), true)

	generated := time.Now().UTC()
	page := 0
	newPage := func() {
		page++
		doc.AddPage()
		y := header(doc, rep, brand, r, g, b, page == 1)
		doc.SetY(y)
		tableHeader(doc, cols, r, g, b)
	}
	newPage()

	fill := false
	for _, row := range rep.Rows {
		if doc.GetY() > pdfBottomY-8 {
			footer(doc, brand, generated, page)
			newPage()
			fill = false
		}
		cells := interfaceCells(row)
		if rep.GroupedBy == "customer" {
			cells = customerCells(row)
		}
		rowLine(doc, cols, cells, fill)
		fill = !fill
	}
	if len(rep.Rows) == 0 {
		doc.SetFont("Helvetica", "I", 9)
		doc.SetTextColor(120, 120, 120)
		doc.CellFormat(0, 8, "No interfaces matched this report.", "", 1, "L", false, 0, "")
	}
	footer(doc, brand, generated, page)

	var buf bytes.Buffer
	if err := doc.Output(&buf); err != nil {
		return err
	}
	name := fmt.Sprintf("netinv-bandwidth-%s_%s.pdf",
		rep.From.UTC().Format("20060102T1504"), rep.To.UTC().Format("20060102T1504"))
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, err := w.Write(buf.Bytes())
	return err
}

// header draws the branding band and, on the first page, the report's own
// identity: what it covers and over what window. The window is repeated in the
// filename and in the footer, because a printed page outlives both.
func header(doc *fpdf.Fpdf, rep *Report, brand settings.Branding, r, g, b int, first bool) float64 {
	y := pdfTopY
	textX := pdfMarginX

	if raw, kind := brand.LogoBytes(); raw != nil {
		// Registered from memory under a stable name so the bytes are
		// embedded once and reused on every page.
		doc.RegisterImageOptionsReader("brand-logo",
			fpdf.ImageOptions{ImageType: kind, ReadDpi: false}, bytes.NewReader(raw))
		if info := doc.GetImageInfo("brand-logo"); info != nil && info.Height() > 0 {
			w := pdfLogoMaxH * (info.Width() / info.Height())
			if w > pdfLogoMaxW {
				w = pdfLogoMaxW
			}
			doc.ImageOptions("brand-logo", pdfMarginX, y, w, pdfLogoMaxH, false,
				fpdf.ImageOptions{ImageType: kind}, 0, "")
			textX = pdfMarginX + w + 4
		}
	}

	doc.SetXY(textX, y)
	doc.SetFont("Helvetica", "B", 15)
	doc.SetTextColor(r, g, b)
	doc.CellFormat(120, 7, tr(doc, brand.CompanyOrDefault()), "", 2, "L", false, 0, "")
	if brand.Subtitle != "" {
		doc.SetFont("Helvetica", "", 9)
		doc.SetTextColor(110, 110, 110)
		doc.CellFormat(120, 5, tr(doc, brand.Subtitle), "", 2, "L", false, 0, "")
	}

	// Right-hand block: the title and the period, which is the part a reader
	// checks first and the part a screenshot loses.
	doc.SetXY(pdfPageW/2, y)
	doc.SetFont("Helvetica", "B", 12)
	doc.SetTextColor(40, 40, 40)
	title := "Bandwidth report"
	if rep.GroupedBy == "customer" {
		title = "Bandwidth report by customer"
	}
	doc.CellFormat(pdfPageW/2-pdfMarginX, 6, title, "", 2, "R", false, 0, "")
	doc.SetFont("Helvetica", "", 9)
	doc.SetTextColor(90, 90, 90)
	// Through tr like every other string: the em-dash is outside ASCII, and an
	// untranslated one renders as mojibake on the line a reader checks first.
	doc.CellFormat(pdfPageW/2-pdfMarginX, 5, tr(doc,
		rep.From.UTC().Format("2006-01-02 15:04")+" — "+
			rep.To.UTC().Format("2006-01-02 15:04")+" UTC"), "", 2, "R", false, 0, "")
	if s := scopeLine(rep); s != "" {
		doc.CellFormat(pdfPageW/2-pdfMarginX, 5, tr(doc, s), "", 2, "R", false, 0, "")
	}

	y = maxF(doc.GetY(), pdfTopY+pdfLogoMaxH) + 3
	doc.SetDrawColor(r, g, b)
	doc.SetLineWidth(0.6)
	doc.Line(pdfMarginX, y, pdfPageW-pdfMarginX, y)
	if !first {
		return y + 4
	}
	if rep.Truncated {
		// A truncated report that does not say so is a wrong report.
		doc.SetXY(pdfMarginX, y+1.5)
		doc.SetFont("Helvetica", "I", 8)
		doc.SetTextColor(180, 90, 20)
		doc.CellFormat(0, 5, "Truncated: more interfaces matched than the row limit allows.",
			"", 1, "L", false, 0, "")
		return doc.GetY() + 1
	}
	return y + 4
}

// scopeLine says what was selected, so a report cannot be mistaken for the
// whole fleet when it was one customer, or the reverse.
func scopeLine(rep *Report) string {
	var parts []string
	if rep.Customer != "" {
		parts = append(parts, "customer: "+rep.Customer)
	}
	if rep.Query != "" {
		parts = append(parts, "match: "+rep.Query)
	}
	if len(parts) == 0 {
		return "all monitored interfaces"
	}
	return strings.Join(parts, "   ")
}

func tableHeader(doc *fpdf.Fpdf, cols []pdfCol, r, g, b int) {
	doc.SetFont("Helvetica", "B", 7.5)
	doc.SetFillColor(r, g, b)
	doc.SetTextColor(255, 255, 255)
	doc.SetDrawColor(r, g, b)
	doc.SetLineWidth(0.1)
	doc.SetX(pdfMarginX)
	for _, c := range cols {
		align := "L"
		if c.right {
			align = "R"
		}
		doc.CellFormat(c.width, 6, c.title, "", 0, align, true, 0, "")
	}
	doc.Ln(-1)
}

func rowLine(doc *fpdf.Fpdf, cols []pdfCol, cells []string, fill bool) {
	doc.SetFont("Helvetica", "", 7.5)
	doc.SetTextColor(30, 30, 30)
	// Banding rather than rules: on a table this wide, horizontal lines fight
	// the numbers for attention and a reader still loses the row.
	doc.SetFillColor(244, 246, 249)
	doc.SetX(pdfMarginX)
	for i, c := range cols {
		v := ""
		if i < len(cells) {
			v = cells[i]
		}
		align := "L"
		if c.right {
			align = "R"
		}
		doc.CellFormat(c.width, 5, clip(doc, tr(doc, v), c.width-2), "", 0, align, fill, 0, "")
	}
	doc.Ln(-1)
}

func footer(doc *fpdf.Fpdf, brand settings.Branding, generated time.Time, page int) {
	doc.SetY(pdfPageH - 12)
	doc.SetDrawColor(210, 210, 210)
	doc.SetLineWidth(0.2)
	doc.Line(pdfMarginX, doc.GetY(), pdfPageW-pdfMarginX, doc.GetY())
	doc.SetY(doc.GetY() + 1)
	doc.SetFont("Helvetica", "", 7.5)
	doc.SetTextColor(130, 130, 130)
	doc.SetX(pdfMarginX)
	left := "Generated " + generated.Format("2006-01-02 15:04") + " UTC by NetInv"
	if brand.Footer != "" {
		left = brand.Footer + "   ·   " + left
	}
	doc.CellFormat(pdfPageW/2-pdfMarginX, 5, clip(doc, tr(doc, left), pdfPageW/2-pdfMarginX-2), "", 0, "L", false, 0, "")
	doc.CellFormat(pdfPageW/2-pdfMarginX, 5, fmt.Sprintf("Page %d", page), "", 0, "R", false, 0, "")
}

func interfaceCells(row Row) []string {
	return []string{
		orDash(row.Customer), row.DeviceName, row.Name, row.Alias,
		bps(float64(row.SpeedBPS)),
		bps(row.AvgInBPS), bps(row.AvgOutBPS),
		bps(row.P95InBPS), bps(row.P95OutBPS),
		bps(row.MaxInBPS), bps(row.MaxOutBPS),
		pctCell(row.P95UtilPct),
	}
}

func customerCells(row Row) []string {
	name := row.Customer
	if name == "" {
		// The untagged group is named, not blank: an invoice-shaped report
		// with an anonymous row is how a circuit goes unbilled.
		name = "(untagged)"
	}
	return []string{
		name, fmt.Sprintf("%d", row.Interfaces), bps(float64(row.SpeedBPS)),
		bps(row.AvgInBPS), bps(row.AvgOutBPS),
		bps(row.P95InBPS), bps(row.P95OutBPS),
		bps(row.MaxInBPS), bps(row.MaxOutBPS),
		bytesHuman(row.TotalInBytes),
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func pctCell(v float64) string {
	if v < 0 {
		// Unknown, not zero: ifSpeed is missing on plenty of real ports, and
		// 0% would read as idle.
		return "—"
	}
	return fmt.Sprintf("%.1f%%", v)
}

// bps prints a rate the way a person reads one. A column of raw bits per
// second is a column nobody compares at a glance.
func bps(v float64) string {
	if v <= 0 {
		return "—"
	}
	units := []string{"bps", "kbps", "Mbps", "Gbps", "Tbps"}
	i := 0
	for v >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	if v >= 100 || i == 0 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

func bytesHuman(v float64) string {
	if v <= 0 {
		return "—"
	}
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for v >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	if v >= 100 || i == 0 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// clip trims a value to the column rather than letting fpdf overflow it into
// the neighbour, which silently makes two columns unreadable instead of one.
func clip(doc *fpdf.Fpdf, s string, w float64) string {
	if doc.GetStringWidth(s) <= w {
		return s
	}
	for len(s) > 1 {
		s = s[:len(s)-1]
		if doc.GetStringWidth(s+"…") <= w {
			return s + "…"
		}
	}
	return s
}

// tr converts UTF-8 to the code page fpdf's core fonts use. Without it a
// customer name with an accent renders as mojibake on a document that goes to
// that customer.
func tr(doc *fpdf.Fpdf, s string) string {
	return doc.UnicodeTranslatorFromDescriptor("cp1252")(s)
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
