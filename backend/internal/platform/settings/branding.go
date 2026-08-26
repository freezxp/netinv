package settings

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

// BrandingKey is the settings row holding report branding.
const BrandingKey = "report.branding"

// Branding is what a exported report says about who produced it.
//
// It is deployment configuration rather than a per-report option because the
// answer never varies by report: the company running NetInv is the company on
// every page of every export it hands a customer.
type Branding struct {
	// Company is the name on the header. Empty falls back to "NetInv", which
	// is honest rather than blank — an unbranded export should look
	// unconfigured, not anonymous.
	Company string `json:"company"`
	// Subtitle is the line under it: a department, a region, a support
	// contract reference.
	Subtitle string `json:"subtitle"`
	// Logo is a data: URI holding a PNG, JPEG or GIF. Stored inline rather
	// than as a file because it is small, it belongs to the same row as the
	// rest of the branding, and a logo on a filesystem is a logo that goes
	// missing when the container is replaced.
	Logo string `json:"logo"`
	// Footer prints at the bottom of every page — a confidentiality notice,
	// a contact address, a document reference.
	Footer string `json:"footer"`
	// Accent colours the rules and table header, as #rrggbb.
	Accent string `json:"accent"`

	// The fields below are layout rather than identity: what the exported
	// page *contains*, as opposed to who it says produced it. They live in
	// the same row because they answer the same question — what does an
	// export from this deployment look like — and because a deployment that
	// hands reports to customers configures both at the same sitting.

	// Columns selects and orders the per-interface table's columns by key
	// (see ReportColumns). Empty means DefaultReportColumns, which names an
	// interface by the alias alone: the alias is what an operator curates
	// and what a customer recognises, while the device name and ifName are
	// internal topology that a customer-facing page should not carry.
	//
	// The grouped-by-customer table is not configurable this way — it has a
	// different shape, one row per customer, with no interface to name.
	Columns []string `json:"columns"`

	// Charts adds a traffic chart per row after the summary table:
	// "each" (the default) or "none". On a grouped report the chart is the
	// customer's combined traffic, which is the series the summary row's
	// p95 and peak were computed from.
	Charts string `json:"charts"`

	// ChartSize is "compact" — two charts to a row, the default — or
	// "full", one across the page for a report of a handful of circuits.
	ChartSize string `json:"chart_size"`
}

// ReportColumn is one selectable column of the per-interface table. Width is
// a proportion rather than millimetres: the chosen subset is scaled to fill
// the page, so removing a column widens the rest instead of leaving a gap.
type ReportColumn struct {
	Key   string
	Title string
	Width float64
	Right bool
}

// ReportColumns is every column an operator may put on the interface table,
// in the order the picker offers them.
var ReportColumns = []ReportColumn{
	{"customer", "Customer", 34, false},
	{"device", "Device", 32, false},
	{"interface", "Interface", 24, false},
	{"alias", "Alias", 52, false},
	{"descr", "Description", 40, false},
	{"speed", "Speed", 20, true},
	{"avg_in", "Avg in", 21, true},
	{"avg_out", "Avg out", 21, true},
	{"p95_in", "P95 in", 21, true},
	{"p95_out", "P95 out", 21, true},
	{"peak_in", "Peak in", 21, true},
	{"peak_out", "Peak out", 21, true},
	{"total_in", "Total in", 22, true},
	{"total_out", "Total out", 22, true},
	{"util_p95", "Util p95", 21, true},
}

// DefaultReportColumns is the table as it prints when nobody has chosen:
// identity by alias, then the rates a report is read for.
var DefaultReportColumns = []string{
	"customer", "alias", "speed",
	"avg_in", "avg_out", "p95_in", "p95_out",
	"peak_in", "peak_out", "util_p95",
}

// ColumnsOrDefault resolves the configured columns, dropping any key that is
// no longer known rather than failing an export over it. A configuration that
// resolves to nothing falls back to the default: a table with no columns is
// not a smaller report, it is a broken one.
func (b Branding) ColumnsOrDefault() []ReportColumn {
	if out := columnsByKey(b.Columns); len(out) > 0 {
		return out
	}
	return columnsByKey(DefaultReportColumns)
}

func columnsByKey(keys []string) []ReportColumn {
	out := make([]ReportColumn, 0, len(keys))
	for _, k := range keys {
		for _, c := range ReportColumns {
			if c.Key == k {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// ChartsEnabled reports whether the export carries per-row charts. The zero
// value means "not configured", and the answer for an unconfigured deployment
// is yes: a report of what a circuit did over a month is read alongside the
// shape of it, and someone who does not want the pages turns them off.
func (b Branding) ChartsEnabled() bool {
	return strings.TrimSpace(b.Charts) != "none"
}

// ChartsPerRow is how many charts sit side by side.
func (b Branding) ChartsPerRow() int {
	if strings.TrimSpace(b.ChartSize) == "full" {
		return 1
	}
	return 2
}

// CompanyOrDefault is the name to print.
func (b Branding) CompanyOrDefault() string {
	if strings.TrimSpace(b.Company) == "" {
		return "NetInv"
	}
	return b.Company
}

// AccentRGB decodes Accent, falling back to a slate that reads on both white
// paper and a screen. Parsing here rather than at the call site keeps the PDF
// writer from having to care whether the setting was ever configured.
func (b Branding) AccentRGB() (int, int, int) {
	m := hexColour.FindStringSubmatch(strings.TrimSpace(b.Accent))
	if m == nil {
		return 51, 65, 85
	}
	var v [3]int
	for i := range v {
		v[i] = hexPair(m[i+1])
	}
	return v[0], v[1], v[2]
}

func hexPair(s string) int {
	n := 0
	for _, c := range s {
		n *= 16
		switch {
		case c >= '0' && c <= '9':
			n += int(c - '0')
		case c >= 'a' && c <= 'f':
			n += int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			n += int(c-'A') + 10
		}
	}
	return n
}

var hexColour = regexp.MustCompile(`^#([0-9a-fA-F]{2})([0-9a-fA-F]{2})([0-9a-fA-F]{2})$`)

// dataURI splits a data: URI into its media type and decoded bytes.
var dataURI = regexp.MustCompile(`^data:(image/(?:png|jpeg|jpg|gif));base64,(.+)$`)

// LogoBytes decodes the logo for the renderer. The second return is the image
// type as fpdf names it; an absent or unreadable logo yields nil, and callers
// print the text header alone rather than failing the export — a report that
// refuses to render because a logo is malformed is worse than one without it.
func (b Branding) LogoBytes() ([]byte, string) {
	m := dataURI.FindStringSubmatch(strings.TrimSpace(b.Logo))
	if m == nil {
		return nil, ""
	}
	raw, err := base64.StdEncoding.DecodeString(m[2])
	if err != nil {
		return nil, ""
	}
	kind := strings.TrimPrefix(m[1], "image/")
	if kind == "jpg" {
		kind = "jpeg"
	}
	return raw, kind
}

// maxLogoBytes caps the decoded image. A logo is embedded in every page of
// every export and stored in a settings row that is read on each render, so a
// 12 MB PNG someone dragged in from a press kit is a cost paid over and over.
const maxLogoBytes = 512 * 1024

const maxBrandTextLen = 200

func (s *Store) GetBranding(ctx context.Context) (Branding, error) {
	var raw []byte
	err := s.Pool.QueryRow(ctx,
		`SELECT value FROM config.settings WHERE key = $1`, BrandingKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		// Never configured is not an error: exports still render, unbranded.
		return Branding{}, nil
	}
	if err != nil {
		return Branding{}, errx.Wrap(errx.KindTransient, err, "read branding setting")
	}
	var b Branding
	if err := json.Unmarshal(raw, &b); err != nil {
		return Branding{}, errx.Wrap(errx.KindInternal, err, "decode branding setting")
	}
	return b, nil
}

// PutBranding validates and stores the branding.
func (s *Store) PutBranding(ctx context.Context, b Branding, actor string) (Branding, error) {
	b, err := b.normalized()
	if err != nil {
		return Branding{}, err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return Branding{}, errx.Wrap(errx.KindInternal, err, "encode branding setting")
	}
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO config.settings (key, tenant_id, value, value_schema, updated_by, updated_at)
		VALUES ($1, 't_default', $2, 'netinv.report.branding/1', nullif($3,''), now())
		ON CONFLICT (key) DO UPDATE SET
			value = excluded.value, updated_by = excluded.updated_by,
			updated_at = excluded.updated_at`, BrandingKey, raw, actor); err != nil {
		return Branding{}, errx.Wrap(errx.KindTransient, err, "write branding setting")
	}
	return b, nil
}

// normalized trims and checks the settings, separately from storing them so
// the rules can be exercised without a database — which is where every one of
// them can actually go wrong.
func (b Branding) normalized() (Branding, error) {
	b.Company = strings.TrimSpace(b.Company)
	b.Subtitle = strings.TrimSpace(b.Subtitle)
	b.Footer = strings.TrimSpace(b.Footer)
	b.Accent = strings.TrimSpace(b.Accent)
	b.Logo = strings.TrimSpace(b.Logo)

	for name, v := range map[string]string{
		"company": b.Company, "subtitle": b.Subtitle, "footer": b.Footer,
	} {
		if len(v) > maxBrandTextLen {
			return Branding{}, errx.New(errx.KindInvalid,
				"%s is longer than %d characters", name, maxBrandTextLen)
		}
	}
	if b.Accent != "" && !hexColour.MatchString(b.Accent) {
		return Branding{}, errx.New(errx.KindInvalid,
			"accent must be a #rrggbb colour, got %q", b.Accent)
	}
	switch strings.TrimSpace(b.Charts) {
	case "", "each", "none":
	default:
		return Branding{}, errx.New(errx.KindInvalid,
			"charts must be \"each\" or \"none\", got %q", b.Charts)
	}
	switch strings.TrimSpace(b.ChartSize) {
	case "", "compact", "full":
	default:
		return Branding{}, errx.New(errx.KindInvalid,
			"chart_size must be \"compact\" or \"full\", got %q", b.ChartSize)
	}
	// An unknown column key is refused rather than dropped. Dropping it would
	// silently print a narrower table than the operator configured, and the
	// only way to notice is to compare an export against the form.
	seen := map[string]bool{}
	for _, k := range b.Columns {
		if len(columnsByKey([]string{k})) == 0 {
			return Branding{}, errx.New(errx.KindInvalid, "unknown report column %q", k)
		}
		if seen[k] {
			return Branding{}, errx.New(errx.KindInvalid, "report column %q is listed twice", k)
		}
		seen[k] = true
	}
	if b.Logo != "" {
		raw, kind := b.LogoBytes()
		if kind == "" {
			return Branding{}, errx.New(errx.KindInvalid,
				"logo must be a data: URI holding a PNG, JPEG or GIF")
		}
		if len(raw) > maxLogoBytes {
			return Branding{}, errx.New(errx.KindInvalid,
				"logo is %d KB; the limit is %d KB", len(raw)/1024, maxLogoBytes/1024)
		}
	}
	return b, nil
}
