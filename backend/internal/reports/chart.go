package reports

import (
	"fmt"
	"math"
	"time"

	"github.com/go-pdf/fpdf"
)

// Drawing one traffic chart.
//
// Vector rather than a rendered image: a PDF chart drawn as lines stays sharp
// at any zoom and on paper, weighs a few kilobytes against a few hundred, and
// needs no image toolchain in the api container. The cost is that everything
// here — axes, gridlines, the gaps where collection stopped — is drawn by
// hand, which is why the geometry is spelled out rather than inferred.

const (
	chartHeadH = 8.5  // title and subtitle rows above the plot
	chartAxisH = 4.5  // room under the plot for the time labels
	chartGutW  = 17.0 // left gutter for the rate labels
	chartGapX  = 7.0  // between two charts side by side
	chartGapY  = 6.0  // between rows of charts
	chartH     = 46.0
)

// outR, outG, outB colour the outbound series. Fixed rather than derived from
// the accent: the two directions have to be told apart at a glance, and a
// second colour computed from the first is exactly the one that cannot be.
const (
	outR, outG, outB = 217, 119, 6
)

// drawChart renders one chart into the box at (x, y).
//
// row supplies the numbers already printed in the table — the chart repeats
// the p95 beside the shape it came from, because the two are read together
// and a reader should not have to hold a figure in their head while turning
// back a page.
func drawChart(doc *fpdf.Fpdf, x, y, w float64, c Chart, row Row,
	from, to time.Time, r, g, b int) {
	// Title row.
	doc.SetXY(x, y)
	doc.SetFont("Helvetica", "B", 8)
	doc.SetTextColor(30, 30, 30)
	doc.CellFormat(w-56, 4.5, clip(doc, tr(doc, c.Title), w-58), "", 0, "L", false, 0, "")
	doc.SetFont("Helvetica", "", 7)
	doc.SetTextColor(110, 110, 110)
	doc.CellFormat(56, 4.5, tr(doc, fmt.Sprintf("p95  in %s   out %s",
		bps(row.P95InBPS), bps(row.P95OutBPS))), "", 0, "R", false, 0, "")

	// Subtitle row, with the legend on the right.
	doc.SetXY(x, y+4)
	doc.SetFont("Helvetica", "", 7)
	doc.SetTextColor(140, 140, 140)
	doc.CellFormat(w-56, 4, clip(doc, tr(doc, c.Subtitle), w-58), "", 0, "L", false, 0, "")
	legend(doc, x+w-40, y+5.2, r, g, b)

	plotY := y + chartHeadH
	plotH := chartH - chartHeadH - chartAxisH
	plotX := x + chartGutW
	plotW := w - chartGutW

	doc.SetDrawColor(222, 226, 232)
	doc.SetLineWidth(0.2)
	doc.Rect(plotX, plotY, plotW, plotH, "D")

	if c.Empty() {
		doc.SetXY(plotX, plotY+plotH/2-2.5)
		doc.SetFont("Helvetica", "I", 7.5)
		doc.SetTextColor(150, 150, 150)
		doc.CellFormat(plotW, 5, "No traffic data in this period", "", 0, "C", false, 0, "")
		timeAxis(doc, plotX, plotY+plotH, plotW, from, to)
		return
	}

	top := niceCeil(math.Max(seriesMax(c.In), seriesMax(c.Out)))
	gridlines(doc, plotX, plotY, plotW, plotH, top)

	xf := func(t int64) float64 {
		span := float64(to.Unix() - from.Unix())
		if span <= 0 {
			return plotX
		}
		f := float64(t-from.Unix()) / span
		return plotX + math.Min(math.Max(f, 0), 1)*plotW
	}
	yf := func(v float64) float64 {
		if top <= 0 {
			return plotY + plotH
		}
		return plotY + plotH - math.Min(v/top, 1)*plotH
	}

	// Inbound is filled as well as stroked. Two stroked lines of equal weight
	// read as a tangle on a small chart; one filled band with a line over it
	// reads as a volume with a second series across it.
	tr8, tg8, tb8 := tint(r, g, b)
	for _, seg := range segments(c.In) {
		fillUnder(doc, seg, xf, yf, plotY+plotH, tr8, tg8, tb8)
	}
	stroke(doc, c.In, xf, yf, r, g, b, 0.4)
	stroke(doc, c.Out, xf, yf, outR, outG, outB, 0.4)

	timeAxis(doc, plotX, plotY+plotH, plotW, from, to)
}

// legend names the two directions. Without it the colours are a convention the
// reader has to be told, and the person this page is sent to was not.
func legend(doc *fpdf.Fpdf, x, y float64, r, g, b int) {
	doc.SetFont("Helvetica", "", 6.5)
	doc.SetLineWidth(0.8)
	doc.SetDrawColor(r, g, b)
	doc.Line(x, y, x+4, y)
	doc.SetTextColor(110, 110, 110)
	doc.SetXY(x+5, y-2)
	doc.CellFormat(9, 4, "In", "", 0, "L", false, 0, "")
	doc.SetDrawColor(outR, outG, outB)
	doc.Line(x+16, y, x+20, y)
	doc.SetXY(x+21, y-2)
	doc.CellFormat(9, 4, "Out", "", 0, "L", false, 0, "")
}

// gridlines draws the horizontal rules and labels them. Three lines — zero,
// half, full — because a 33 mm-tall plot with five is a grey box.
func gridlines(doc *fpdf.Fpdf, x, y, w, h, top float64) {
	doc.SetFont("Helvetica", "", 6.5)
	doc.SetTextColor(140, 140, 140)
	doc.SetDrawColor(232, 235, 240)
	doc.SetLineWidth(0.15)
	for _, f := range []float64{0, 0.5, 1} {
		ly := y + h - f*h
		if f > 0 {
			doc.Line(x, ly, x+w, ly)
		}
		doc.SetXY(x-chartGutW, ly-2)
		doc.CellFormat(chartGutW-1.5, 4, bpsAxis(f*top), "", 0, "R", false, 0, "")
	}
}

// timeAxis labels the window's ends. The middle is deliberately unlabelled: on
// a chart this wide a third label collides with one of the others at some
// window length, and the ends are what a reader checks.
func timeAxis(doc *fpdf.Fpdf, x, y, w float64, from, to time.Time) {
	doc.SetFont("Helvetica", "", 6.5)
	doc.SetTextColor(140, 140, 140)
	layout := "2006-01-02 15:04"
	if to.Sub(from) >= 3*24*time.Hour {
		// Past a few days the clock time is noise — the reader is placing the
		// chart in a month, not in an afternoon.
		layout = "2006-01-02"
	}
	doc.SetXY(x, y+0.5)
	doc.CellFormat(w/2, 4, from.UTC().Format(layout), "", 0, "L", false, 0, "")
	doc.SetXY(x+w/2, y+0.5)
	doc.CellFormat(w/2, 4, to.UTC().Format(layout)+" UTC", "", 0, "R", false, 0, "")
}

func stroke(doc *fpdf.Fpdf, pts []Point, xf func(int64) float64,
	yf func(float64) float64, r, g, b int, width float64) {
	doc.SetDrawColor(r, g, b)
	doc.SetLineWidth(width)
	for _, seg := range segments(pts) {
		if len(seg) == 1 {
			// A lone sample between two gaps is still a measurement: drawn as
			// a dot rather than dropped, so a sparse series is visibly sparse
			// instead of visibly empty.
			doc.Circle(xf(seg[0].T), yf(seg[0].V), width, "F")
			continue
		}
		doc.MoveTo(xf(seg[0].T), yf(seg[0].V))
		for _, p := range seg[1:] {
			doc.LineTo(xf(p.T), yf(p.V))
		}
		doc.DrawPath("D")
	}
}

func fillUnder(doc *fpdf.Fpdf, seg []Point, xf func(int64) float64,
	yf func(float64) float64, base float64, r, g, b int) {
	if len(seg) < 2 {
		return
	}
	doc.SetFillColor(r, g, b)
	doc.MoveTo(xf(seg[0].T), base)
	for _, p := range seg {
		doc.LineTo(xf(p.T), yf(p.V))
	}
	doc.LineTo(xf(seg[len(seg)-1].T), base)
	doc.ClosePath()
	doc.DrawPath("F")
}

// segments splits a series where collection stopped. Joining across a gap
// draws a straight line through an outage, which reads as steady traffic over
// exactly the period there was no measurement at all.
func segments(pts []Point) [][]Point {
	if len(pts) == 0 {
		return nil
	}
	// The expected spacing is the smallest gap present: the range query asks
	// for a fixed step, so anything materially wider is missing samples.
	base := int64(math.MaxInt64)
	for i := 1; i < len(pts); i++ {
		if d := pts[i].T - pts[i-1].T; d > 0 && d < base {
			base = d
		}
	}
	if base == int64(math.MaxInt64) {
		base = 0
	}
	var out [][]Point
	cur := []Point{pts[0]}
	for i := 1; i < len(pts); i++ {
		if base > 0 && pts[i].T-pts[i-1].T > 2*base {
			out = append(out, cur)
			cur = nil
		}
		cur = append(cur, pts[i])
	}
	return append(out, cur)
}

func seriesMax(pts []Point) float64 {
	m := 0.0
	for _, p := range pts {
		if p.V > m {
			m = p.V
		}
	}
	return m
}

// niceCeil rounds an axis top up to 1, 2 or 5 times a power of ten, so the
// half-way gridline lands on a number a reader can divide by.
func niceCeil(v float64) float64 {
	if v <= 0 {
		return 1
	}
	mag := math.Pow(10, math.Floor(math.Log10(v)))
	switch f := v / mag; {
	case f <= 1:
		return mag
	case f <= 2:
		return 2 * mag
	case f <= 5:
		return 5 * mag
	default:
		return 10 * mag
	}
}

// tint lightens the accent for the fill under the inbound line. Computed from
// the accent rather than fixed, so a deployment's colour carries through, and
// light enough that the gridlines stay visible behind it.
func tint(r, g, b int) (int, int, int) {
	f := func(c int) int { return 255 - int(float64(255-c)*0.16) }
	return f(r), f(g), f(b)
}

// bpsAxis is the axis label. Zero prints as "0" rather than as a dash: on an
// axis it is a position, not a missing value.
func bpsAxis(v float64) string {
	if v <= 0 {
		return "0"
	}
	return bps(v)
}
