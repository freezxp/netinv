package geo

import (
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Synthetic data only: real ip2asn/DB-IP files are never committed (ADR-026).
const asnTSV = "1.0.0.0\t1.0.0.255\t13335\tUS\tCLOUDFLARENET\n" +
	"1.0.1.0\t1.0.3.255\t0\tNone\tNot routed\n" +
	"8.8.8.0\t8.8.8.255\t15169\tUS\tGOOGLE\n" +
	"8.8.4.0\t8.8.4.255\t15169\tUS\tGOOGLE\n" + // out of order, and the same AS again
	"9.9.9.0\t9.9.9.255\t19281\tCH\tQUAD9-AS-1\n" +
	"2606:4700::\t2606:4700:ffff:ffff:ffff:ffff:ffff:ffff\t13335\tUS\tCLOUDFLARENET\n" +
	"not an address\t1.2.3.4\t1\tUS\tbroken\n" +
	"5.5.5.5\t5.5.5.0\t7\tUS\tend before start\n" +
	"1.2.3.0\t1.2.3.255\tnotanumber\tUS\tbad asn\n" +
	"short line\n"

const countryCSV = "0.0.0.0,0.255.255.255,ZZ\n" +
	"1.0.0.0,1.0.0.255,AU\n" +
	"8.8.8.0,8.8.8.255,US\n" +
	"2606:4700::,2606:4700:ffff:ffff:ffff:ffff:ffff:ffff,US\n" +
	"garbage,line\n" +
	"10.0.0.0,10.0.0.255,toolong\n"

func writeGz(t testing.TB, path, body string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if _, err := io.WriteString(gz, body); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func loadBoth(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	writeGz(t, filepath.Join(dir, ASNFile+".gz"), asnTSV)
	writeGz(t, filepath.Join(dir, CountryFile+".gz"), countryCSV)
	db, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestLookup(t *testing.T) {
	db := loadBoth(t)
	cases := []struct {
		addr string
		want Info
	}{
		// DB-IP's location wins over the AS's registration country.
		{"1.0.0.1", Info{13335, "CLOUDFLARENET", "AU"}},
		{"1.0.0.0", Info{13335, "CLOUDFLARENET", "AU"}},   // range start
		{"1.0.0.255", Info{13335, "CLOUDFLARENET", "AU"}}, // range end
		{"1.0.1.0", Info{}},                      // AS 0: unrouted, not kept
		{"0.255.255.255", Info{}},                // ZZ: no country
		{"8.8.4.4", Info{15169, "GOOGLE", "US"}}, // fallback to ip2asn country
		{"8.8.8.8", Info{15169, "GOOGLE", "US"}},
		{"8.8.9.0", Info{}},                                    // just past a range end
		{"8.8.7.255", Info{}},                                  // just before a range start
		{"9.9.9.9", Info{19281, "QUAD9-AS-1", "CH"}},           // country only from ip2asn
		{"::ffff:1.0.0.1", Info{13335, "CLOUDFLARENET", "AU"}}, // v4-mapped
		{"2606:4700::1111", Info{13335, "CLOUDFLARENET", "US"}},
		{"2606:4700:ffff:ffff:ffff:ffff:ffff:ffff", Info{13335, "CLOUDFLARENET", "US"}},
		{"2606:4701::", Info{}},
		{"::1", Info{}},
	}
	for _, c := range cases {
		if got := db.Lookup(netip.MustParseAddr(c.addr)); got != c.want {
			t.Errorf("Lookup(%s) = %+v, want %+v", c.addr, got, c.want)
		}
	}
	if got := db.Lookup(netip.Addr{}); got != (Info{}) {
		t.Errorf("invalid address: %+v", got)
	}
}

func TestLoadCountsAndSkips(t *testing.T) {
	db := loadBoth(t)
	asn, cc := db.Loaded()
	if asn != 5 || cc != 3 {
		t.Fatalf("Loaded() = %d, %d; want 5 AS ranges and 3 country ranges", asn, cc)
	}
	if db.SkippedASN != 4 {
		t.Errorf("SkippedASN = %d, want 4", db.SkippedASN)
	}
	// "toolong" is malformed; ZZ is valid-but-empty and not counted.
	if db.SkippedCountry != 2 {
		t.Errorf("SkippedCountry = %d, want 2", db.SkippedCountry)
	}
	if !db.HasASN() || !db.HasCountry() {
		t.Error("expected both halves loaded")
	}
}

// One name per AS, however many ranges it announces.
func TestASNamesInterned(t *testing.T) {
	db := loadBoth(t)
	if len(db.asNames) != 3 {
		t.Fatalf("asNames holds %d entries, want 3: %v", len(db.asNames), db.asNames)
	}
}

func TestMissingFiles(t *testing.T) {
	db, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if db == nil || db.HasASN() || db.HasCountry() {
		t.Fatalf("empty dir: want empty non-nil DB, got %+v", db)
	}
	if got := db.Lookup(netip.MustParseAddr("1.1.1.1")); got != (Info{}) {
		t.Errorf("empty DB lookup = %+v", got)
	}
}

func TestOnlyOneSource(t *testing.T) {
	dir := t.TempDir()
	// Uncompressed is accepted too.
	if err := os.WriteFile(filepath.Join(dir, ASNFile), []byte(asnTSV), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !db.HasASN() || db.HasCountry() {
		t.Fatal("want AS data only")
	}
	if got := db.Lookup(netip.MustParseAddr("1.0.0.1")); got.Country != "US" {
		t.Errorf("without DB-IP the AS registration country is the fallback, got %q", got.Country)
	}
}

func TestPresentButEmptyIsAnError(t *testing.T) {
	dir := t.TempDir()
	writeGz(t, filepath.Join(dir, CountryFile+".gz"), "this is not,a geo file\n")
	if _, err := Load(dir); err == nil {
		t.Fatal("a file with no valid range must fail the load")
	}
}

func TestOverlongLineRejected(t *testing.T) {
	dir := t.TempDir()
	writeGz(t, filepath.Join(dir, ASNFile+".gz"), strings.Repeat("x", maxLine+10)+"\n")
	if _, err := Load(dir); err == nil {
		t.Fatal("an overlong line must fail the load")
	}
}

func TestNilDB(t *testing.T) {
	var db *DB
	if got := db.Lookup(netip.MustParseAddr("1.1.1.1")); got != (Info{}) {
		t.Errorf("nil lookup = %+v", got)
	}
	if db.HasASN() || db.HasCountry() {
		t.Error("nil DB has nothing")
	}
	if a, c := db.Loaded(); a != 0 || c != 0 {
		t.Error("nil DB loaded nothing")
	}
	if db.ASName(13335) != "" {
		t.Error("nil DB knows no AS names")
	}
}

func TestWatchReloads(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var got []*DB
	set := func(d *DB) { mu.Lock(); got = append(got, d); mu.Unlock() }
	count := func() (int, *DB) {
		mu.Lock()
		defer mu.Unlock()
		if len(got) == 0 {
			return 0, nil
		}
		return len(got), got[len(got)-1]
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		Watch(ctx, dir, 10*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)), set)
		close(done)
	}()

	waitFor := func(n int) *DB {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if c, d := count(); c >= n {
				return d
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("Watch did not call set %d times", n)
		return nil
	}
	// First pass with no files: an empty DB, not nil.
	if d := waitFor(1); d == nil || d.HasASN() {
		t.Fatalf("first load: %+v", d)
	}
	writeGz(t, filepath.Join(dir, ASNFile+".gz"), asnTSV)
	if d := waitFor(2); !d.HasASN() {
		t.Fatal("reload after the file appeared did not pick it up")
	}
	// A broken replacement keeps the previous DB.
	writeGz(t, filepath.Join(dir, ASNFile+".gz"), "nothing valid here\n")
	time.Sleep(100 * time.Millisecond)
	if c, d := count(); c != 2 || !d.HasASN() {
		t.Fatalf("a failed reload must not replace the DB (calls=%d)", c)
	}
	cancel()
	<-done
}

func BenchmarkLookup(b *testing.B) {
	dir := b.TempDir()
	// A table of realistic size: 200k IPv4 /24s.
	var sb strings.Builder
	for i := 0; i < 200_000; i++ {
		a := netip.AddrFrom4([4]byte{byte(i >> 16), byte(i >> 8), byte(i), 0})
		e := netip.AddrFrom4([4]byte{byte(i >> 16), byte(i >> 8), byte(i), 255})
		sb.WriteString(a.String() + "\t" + e.String() + "\t64512\tUS\tTEST\n")
	}
	writeGz(b, filepath.Join(dir, ASNFile+".gz"), sb.String())
	db, err := Load(dir)
	if err != nil {
		b.Fatal(err)
	}
	addr := netip.MustParseAddr("1.134.160.7")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = db.Lookup(addr)
	}
}

func TestASName(t *testing.T) {
	db := loadBoth(t)
	cases := map[uint32]string{13335: "CLOUDFLARENET", 15169: "GOOGLE", 0: "", 64512: ""}
	for asn, want := range cases {
		if got := db.ASName(asn); got != want {
			t.Errorf("ASName(%d) = %q, want %q", asn, got, want)
		}
	}
	empty, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if empty.ASName(13335) != "" {
		t.Error("a DB without AS data knows no names")
	}
}
