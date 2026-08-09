package config

import (
	"testing"
	"time"
)

func TestParseRetentionAcceptsVictoriaMetricsUnits(t *testing.T) {
	day := 24 * time.Hour
	cases := map[string]time.Duration{
		"90d":  90 * day,
		"2y":   730 * day,
		"1y":   365 * day,
		"52w":  364 * day,
		"18mo": 18 * 31 * day, // months are 31 days, as VictoriaMetrics treats them
		"720h": 720 * time.Hour,
	}
	for in, want := range cases {
		got, err := ParseRetention(in)
		if err != nil {
			t.Errorf("ParseRetention(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseRetention(%q) = %v, want %v", in, got, want)
		}
	}
}

// "mo" has to be matched before "m", or 18mo parses as 18 minutes and the
// query ceiling collapses from eighteen months to under an hour.
func TestParseRetentionPrefersMonthsOverMinutes(t *testing.T) {
	mo, err := ParseRetention("6mo")
	if err != nil {
		t.Fatal(err)
	}
	min, err := ParseRetention("6m")
	if err != nil {
		t.Fatal(err)
	}
	if mo <= min {
		t.Errorf("6mo = %v, 6m = %v — months must not be read as minutes", mo, min)
	}
}

func TestParseRetentionRejectsNonsense(t *testing.T) {
	for _, in := range []string{"", "d", "-5d", "0d", "90", "ninety days", "90x"} {
		if got, err := ParseRetention(in); err == nil {
			t.Errorf("ParseRetention(%q) = %v, want an error", in, got)
		}
	}
}

// An unset or unparseable value must fall back rather than yield zero, which
// the proxy would read as "no limit configured" and could turn into either an
// open-ended query or an immediate rejection.
func TestRetentionFallsBackToNinetyDays(t *testing.T) {
	t.Setenv("NETINV_VM_RETENTION", "")
	if got, want := Retention(), 90*24*time.Hour; got != want {
		t.Errorf("unset: Retention() = %v, want %v", got, want)
	}
	t.Setenv("NETINV_VM_RETENTION", "not-a-duration")
	if got, want := Retention(), 90*24*time.Hour; got != want {
		t.Errorf("invalid: Retention() = %v, want %v", got, want)
	}
	t.Setenv("NETINV_VM_RETENTION", "2y")
	if got, want := Retention(), 730*24*time.Hour; got != want {
		t.Errorf("2y: Retention() = %v, want %v", got, want)
	}
}
