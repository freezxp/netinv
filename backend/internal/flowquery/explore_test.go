package flowquery

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/freezxp/netinv/backend/internal/flowstore"
)

func TestExplorerResolvesDevicesAndSitesToExporters(t *testing.T) {
	e := &Explorer{
		CH: flowstore.NewClient("http://unused", "", ""), Book: book,
		Retention: flowstore.DefaultRetention,
		Now:       func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) },
	}
	cases := map[string][]string{
		`{"devices":["d_gw1"]}`:  {"192.0.2.1", "198.51.100.7"}, // management + claimed
		`{"sites":["s_b"]}`:      {"192.0.2.2"},
		`{"devices":["d_nope"]}`: {},
		`{"group_by":["proto"]}`: nil,
	}
	for body, want := range cases {
		r := httptest.NewRequest(http.MethodPost, "/flow/explore", strings.NewReader(body))
		_, q, _, err := e.parse(r)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if want == nil {
			if q.Exporters != nil {
				t.Errorf("%s: exporters %v, want no restriction", body, q.Exporters)
			}
			continue
		}
		var got []string
		for _, a := range q.Exporters {
			got = append(got, a.String())
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: exporters %v, want %v", body, got, want)
		}
		// An unknown device must restrict to nothing, not to everything.
		if len(want) == 0 && (q.Exporters == nil || len(q.Exporters) != 0) {
			t.Errorf("%s: an unknown device must match nothing", body)
		}
	}
}

func TestExplorerRawCutoffFollowsRetention(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	e := &Explorer{
		CH: flowstore.NewClient("http://unused", "", ""), Book: book,
		Retention: flowstore.Retention{RawDays: 7, RollupDays: 90},
		Now:       func() time.Time { return now },
	}
	r := httptest.NewRequest(http.MethodPost, "/flow/explore", strings.NewReader(`{}`))
	_, q, _, err := e.parse(r)
	if err != nil {
		t.Fatal(err)
	}
	if !q.RawCutoff.Equal(now.Add(-7 * 24 * time.Hour)) {
		t.Fatalf("raw cutoff %s", q.RawCutoff)
	}
	r = httptest.NewRequest(http.MethodPost, "/flow/explore",
		strings.NewReader(`{"from":"2026-01-01T00:00:00Z","to":"2026-10-29T00:00:00Z"}`))
	if _, _, _, err := e.parse(r); err == nil {
		t.Fatal("a window longer than the rollup retention must be refused")
	}
}

func TestExplorerWithoutStoreSaysSo(t *testing.T) {
	e := &Explorer{Book: book}
	r := httptest.NewRequest(http.MethodPost, "/flow/explore", strings.NewReader(`{}`))
	if _, _, _, err := e.parse(r); err == nil || !strings.Contains(err.Error(), "NETINV_FLOW_CH_URL") {
		t.Fatalf("want a not-configured error naming the setting, got %v", err)
	}
}
