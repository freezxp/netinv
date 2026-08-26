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
