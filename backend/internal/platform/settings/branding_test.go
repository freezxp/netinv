package settings

import (
	"encoding/base64"
	"strings"
	"testing"
)

const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// An unbranded deployment must produce a usable header rather than a blank
// one: an export with no name on it looks anonymous, not unconfigured.
func TestBrandingFallsBackToAName(t *testing.T) {
	if got := (Branding{}).CompanyOrDefault(); got != "NetInv" {
		t.Fatalf("default company = %q", got)
	}
	if got := (Branding{Company: "  "}).CompanyOrDefault(); got != "NetInv" {
		t.Fatalf("whitespace company = %q", got)
	}
	if got := (Branding{Company: "Acme"}).CompanyOrDefault(); got != "Acme" {
		t.Fatalf("company = %q", got)
	}
}

func TestAccentParsesOrFallsBack(t *testing.T) {
	r, g, b := Branding{Accent: "#1d4ed8"}.AccentRGB()
	if r != 29 || g != 78 || b != 216 {
		t.Fatalf("accent = %d,%d,%d want 29,78,216", r, g, b)
	}
	// Anything unparseable must not produce black-on-black or a panic.
	for _, bad := range []string{"", "blue", "#12345", "1d4ed8", "#gggggg"} {
		r, g, b := Branding{Accent: bad}.AccentRGB()
		if r == 0 && g == 0 && b == 0 {
			t.Fatalf("accent %q fell back to black", bad)
		}
	}
}

func TestLogoBytesAcceptsOnlyImageDataURIs(t *testing.T) {
	good := "data:image/png;base64," + onePixelPNG
	raw, kind := Branding{Logo: good}.LogoBytes()
	if kind != "png" || len(raw) == 0 {
		t.Fatalf("png logo = %q %d bytes", kind, len(raw))
	}
	// jpg is normalised to the name fpdf knows, or the image silently fails
	// to register at render time.
	if _, kind := (Branding{Logo: "data:image/jpg;base64," + onePixelPNG}).LogoBytes(); kind != "jpeg" {
		t.Fatalf("jpg normalised to %q, want jpeg", kind)
	}
	for _, bad := range []string{
		"", "https://example.com/logo.png",
		"data:text/html;base64,PGgxPmhpPC9oMT4=",
		"data:image/svg+xml;base64," + onePixelPNG, // scriptable, not an image we render
		"data:image/png;base64,%%%not-base64%%%",
	} {
		if raw, kind := (Branding{Logo: bad}).LogoBytes(); raw != nil || kind != "" {
			t.Fatalf("accepted %q", bad)
		}
	}
}

// Validation runs without a database: these are the rules that keep a settings
// row from growing to megabytes or a header from running off the page.
func TestBrandingValidationRules(t *testing.T) {
	s := &Store{}
	long := strings.Repeat("x", maxBrandTextLen+1)

	if _, err := s.PutBranding(t.Context(), Branding{Company: long}, "u"); err == nil {
		t.Fatal("an over-long company name was accepted")
	}
	if _, err := s.PutBranding(t.Context(), Branding{Accent: "blue"}, "u"); err == nil {
		t.Fatal("a non-hex accent was accepted")
	}
	if _, err := s.PutBranding(t.Context(), Branding{Logo: "not-a-data-uri"}, "u"); err == nil {
		t.Fatal("a non-data-URI logo was accepted")
	}
	big := base64.StdEncoding.EncodeToString(make([]byte, maxLogoBytes+1))
	if _, err := s.PutBranding(t.Context(),
		Branding{Logo: "data:image/png;base64," + big}, "u"); err == nil {
		t.Fatal("an oversized logo was accepted")
	}
}

// Layout validation. An unknown column key is refused rather than dropped:
// dropping it prints a narrower table than the operator configured, and the
// only way to notice is to compare an export against the form.
func TestReportLayoutValidationRules(t *testing.T) {
	for _, b := range []Branding{
		{Charts: "sometimes"},
		{ChartSize: "enormous"},
		{Columns: []string{"alias", "no-such-column"}},
		{Columns: []string{"alias", "alias"}},
	} {
		if _, err := b.normalized(); err == nil {
			t.Errorf("accepted an invalid layout: %+v", b)
		}
	}
	// The valid shapes, including the unset one an existing row deserializes to.
	for _, b := range []Branding{
		{},
		{Charts: "each", ChartSize: "compact"},
		{Charts: "none", ChartSize: "full", Columns: []string{"alias", "p95_in"}},
	} {
		if _, err := b.normalized(); err != nil {
			t.Errorf("rejected a valid layout %+v: %v", b, err)
		}
	}
}

// An unconfigured deployment gets a table named by alias, with charts on.
func TestReportLayoutDefaults(t *testing.T) {
	var b Branding
	if !b.ChartsEnabled() {
		t.Error("charts should be on until someone turns them off")
	}
	if b.ChartsPerRow() != 2 {
		t.Errorf("default charts per row = %d, want 2", b.ChartsPerRow())
	}
	keys := map[string]bool{}
	for _, c := range b.ColumnsOrDefault() {
		keys[c.Key] = true
	}
	if !keys["alias"] {
		t.Error("the default table must name an interface by its alias")
	}
	for _, gone := range []string{"device", "interface"} {
		if keys[gone] {
			t.Errorf("the default table should not carry %q", gone)
		}
	}
	// A configuration naming only columns that no longer exist falls back
	// rather than printing a report with no columns at all.
	if got := (Branding{Columns: []string{"gone"}}).ColumnsOrDefault(); len(got) == 0 {
		t.Error("an unresolvable column list emptied the table")
	}
}
