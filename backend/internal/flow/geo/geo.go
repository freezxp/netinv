// Package geo enriches flow addresses with an AS number, AS name and country
// from offline databases the operator downloads (ADR-026, doc 34).
//
// Two sources, both read from one directory and both optional:
//
//   - iptoasn.com's ip2asn-combined.tsv(.gz) — PDDL, public domain. TSV:
//     range_start, range_end, AS_number, country_code, AS_description, IPv4
//     and IPv6 in one file. AS 0 marks unrouted space and is not kept.
//   - DB-IP's country lite CSV(.gz) — CC BY 4.0, which obliges any page
//     showing its results to credit "IP Geolocation by DB-IP" with a link to
//     https://db-ip.com. CSV: ip_start, ip_end, country_code; "ZZ" is unknown.
//
// Neither file is in the repository; scripts/fetch-flow-geo.sh downloads them.
// A lookup is a binary search over sorted, non-overlapping ranges. IPv4
// ranges are held as uint32 pairs, which halves the memory of the larger
// half of both files compared with a uniform 128-bit form.
package geo

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// File names Load looks for, each with or without .gz.
const (
	ASNFile     = "ip2asn-combined.tsv"
	CountryFile = "dbip-country-lite.csv"
)

// maxLine bounds one input line. Real lines are under 200 bytes; a file that
// is not what it claims should not be able to make the scanner buffer grow.
const maxLine = 4096

// Info is what is known about one address. Zero values mean unknown.
type Info struct {
	ASN    uint32
	ASName string
	// Country is the ISO 3166 alpha-2 code.
	Country string
}

type v4Range struct {
	start, end uint32
	val        uint32 // ASN, or a packed country code
}

type v6Range struct {
	startHi, startLo uint64
	endHi, endLo     uint64
	val              uint32
}

// table is one source's ranges, IPv4 and IPv6 kept apart.
type table struct {
	v4 []v4Range
	v6 []v6Range
}

func (t *table) len() int { return len(t.v4) + len(t.v6) }

func (t *table) add(start, end netip.Addr, val uint32) bool {
	start, end = start.Unmap(), end.Unmap()
	if start.Is4() != end.Is4() || end.Less(start) {
		return false
	}
	if start.Is4() {
		s, e := start.As4(), end.As4()
		t.v4 = append(t.v4, v4Range{binary.BigEndian.Uint32(s[:]), binary.BigEndian.Uint32(e[:]), val})
		return true
	}
	sh, sl := split(start)
	eh, el := split(end)
	t.v6 = append(t.v6, v6Range{sh, sl, eh, el, val})
	return true
}

func (t *table) sort() {
	sort.Slice(t.v4, func(i, j int) bool { return t.v4[i].start < t.v4[j].start })
	sort.Slice(t.v6, func(i, j int) bool {
		a, b := t.v6[i], t.v6[j]
		return a.startHi < b.startHi || (a.startHi == b.startHi && a.startLo < b.startLo)
	})
}

// find returns the value of the range holding a, and whether one does.
func (t *table) find(a netip.Addr) (uint32, bool) {
	a = a.Unmap()
	if a.Is4() {
		b := a.As4()
		x := binary.BigEndian.Uint32(b[:])
		// Last range starting at or before x.
		i := sort.Search(len(t.v4), func(i int) bool { return t.v4[i].start > x }) - 1
		if i >= 0 && x <= t.v4[i].end {
			return t.v4[i].val, true
		}
		return 0, false
	}
	if !a.Is6() {
		return 0, false
	}
	hi, lo := split(a)
	i := sort.Search(len(t.v6), func(i int) bool {
		r := t.v6[i]
		return r.startHi > hi || (r.startHi == hi && r.startLo > lo)
	}) - 1
	if i < 0 {
		return 0, false
	}
	r := t.v6[i]
	if hi < r.endHi || (hi == r.endHi && lo <= r.endLo) {
		return r.val, true
	}
	return 0, false
}

func split(a netip.Addr) (uint64, uint64) {
	b := a.As16()
	return binary.BigEndian.Uint64(b[:8]), binary.BigEndian.Uint64(b[8:])
}

// DB is a loaded pair of tables. Immutable once returned, so concurrent
// lookups need no lock; a reload builds a new DB and swaps it in.
type DB struct {
	asn     table
	country table
	// asNames interns AS descriptions: one string per AS, not per range.
	asNames map[uint32]string
	// asCountry is the ip2asn registration country per AS, the fallback
	// when DB-IP is not loaded. Registration is not location, so DB-IP wins
	// whenever it has an answer.
	asCountry map[uint32]uint32
	// Skipped counts malformed lines, per file, for the load log.
	SkippedASN, SkippedCountry int
}

// HasASN reports whether AS data was loaded.
func (d *DB) HasASN() bool { return d != nil && d.asn.len() > 0 }

// HasCountry reports whether DB-IP country data was loaded.
func (d *DB) HasCountry() bool { return d != nil && d.country.len() > 0 }

// Loaded returns the number of ranges held from each source.
func (d *DB) Loaded() (asnRanges, countryRanges int) {
	if d == nil {
		return 0, 0
	}
	return d.asn.len(), d.country.len()
}

// ASName returns the name of an AS number, or "" when it is unknown or 0.
// Used for AS numbers the exporter supplied itself, which need a name as much
// as the ones looked up here. Safe on a nil DB.
func (d *DB) ASName(asn uint32) string {
	if d == nil || asn == 0 {
		return ""
	}
	return d.asNames[asn]
}

// Lookup returns what is known about a. Safe on a nil DB.
func (d *DB) Lookup(a netip.Addr) Info {
	var info Info
	if d == nil || !a.IsValid() {
		return info
	}
	if asn, ok := d.asn.find(a); ok {
		info.ASN = asn
		info.ASName = d.asNames[asn]
		if cc, ok := d.asCountry[asn]; ok {
			info.Country = unpackCC(cc)
		}
	}
	if cc, ok := d.country.find(a); ok {
		info.Country = unpackCC(cc)
	}
	return info
}

// packCC stores a two-letter code in a uint32; 0 means none.
func packCC(s string) (uint32, bool) {
	if len(s) != 2 || !isAlpha(s[0]) || !isAlpha(s[1]) {
		return 0, false
	}
	s = strings.ToUpper(s)
	if s == "ZZ" { // DB-IP's "unknown"
		return 0, false
	}
	return uint32(s[0])<<8 | uint32(s[1]), true
}

func unpackCC(v uint32) string {
	if v == 0 {
		return ""
	}
	return string([]byte{byte(v >> 8), byte(v)})
}

func isAlpha(c byte) bool { return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') }

// Load reads whichever source files are present in dir. Both absent is not
// an error — enrichment is optional — and yields an empty DB. A file that is
// present but yields no valid range is an error: it is the wrong file, or
// truncated, and silently running without it would look like enrichment
// working on an address space with no answers.
func Load(dir string) (*DB, error) {
	d := &DB{asNames: map[uint32]string{}, asCountry: map[uint32]uint32{}}
	if f, ok := find(dir, ASNFile); ok {
		n, skipped, err := readFile(f, d.addASN)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, fmt.Errorf("geo: %s holds no valid AS range (%d lines skipped)", f, skipped)
		}
		d.SkippedASN = skipped
	}
	if f, ok := find(dir, CountryFile); ok {
		n, skipped, err := readFile(f, d.addCountry)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, fmt.Errorf("geo: %s holds no valid country range (%d lines skipped)", f, skipped)
		}
		d.SkippedCountry = skipped
	}
	d.asn.sort()
	d.country.sort()
	return d, nil
}

// find prefers the compressed file, which is what the fetch script writes.
func find(dir, name string) (string, bool) {
	for _, p := range []string{filepath.Join(dir, name+".gz"), filepath.Join(dir, name)} {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p, true
		}
	}
	return "", false
}

// addASN parses one ip2asn line. It returns (kept, valid): an AS 0 line is
// valid but not kept, because unrouted space has nothing to say.
func (d *DB) addASN(line string) (bool, bool) {
	f := strings.SplitN(line, "\t", 5)
	if len(f) != 5 {
		return false, false
	}
	start, err1 := netip.ParseAddr(f[0])
	end, err2 := netip.ParseAddr(f[1])
	asn, err3 := strconv.ParseUint(f[2], 10, 32)
	if err1 != nil || err2 != nil || err3 != nil {
		return false, false
	}
	if asn == 0 {
		return false, true
	}
	if !d.asn.add(start, end, uint32(asn)) {
		return false, false
	}
	a := uint32(asn)
	if _, ok := d.asNames[a]; !ok {
		d.asNames[a] = clean(f[4])
		if cc, ok := packCC(f[3]); ok {
			d.asCountry[a] = cc
		}
	}
	return true, true
}

// addCountry parses one DB-IP line.
func (d *DB) addCountry(line string) (bool, bool) {
	f := strings.Split(line, ",")
	if len(f) != 3 {
		return false, false
	}
	start, err1 := netip.ParseAddr(f[0])
	end, err2 := netip.ParseAddr(f[1])
	if err1 != nil || err2 != nil {
		return false, false
	}
	cc, ok := packCC(f[2])
	if !ok {
		// ZZ (unknown) and reserved space are valid lines with no answer.
		return false, len(f[2]) == 2
	}
	if !d.country.add(start, end, cc) {
		return false, false
	}
	return true, true
}

// clean bounds an AS description from the file: printable, at most 96 bytes.
func clean(s string) string {
	s = strings.ToValidUTF8(strings.TrimSpace(s), "")
	if len(s) > 96 {
		s = s[:96]
		s = strings.ToValidUTF8(s, "")
	}
	return s
}

// readFile feeds each line to add, returning how many were kept and how many
// were malformed.
func readFile(path string, add func(string) (kept, valid bool)) (int, int, error) {
	f, err := os.Open(path) //nolint:gosec // path is the operator's configured directory
	if err != nil {
		return 0, 0, fmt.Errorf("geo: %w", err)
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return 0, 0, fmt.Errorf("geo: %s: %w", path, err)
		}
		defer gz.Close()
		r = gz
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 512), maxLine)
	kept, skipped := 0, 0
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" || line[0] == '#' {
			continue
		}
		k, valid := add(line)
		switch {
		case k:
			kept++
		case !valid:
			skipped++
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return 0, 0, fmt.Errorf("geo: %s: a line exceeds %d bytes — not an ip2asn/DB-IP file", path, maxLine)
		}
		return 0, 0, fmt.Errorf("geo: %s: %w", path, err)
	}
	return kept, skipped, nil
}

// Watch reloads the databases when either file's modification time or size
// changes, so a monthly re-download takes effect without a restart. It calls
// set with the first successful load as well, and keeps the previous DB when a
// reload fails — a half-written download must not empty the enrichment.
func Watch(ctx context.Context, dir string, every time.Duration, log *slog.Logger, set func(*DB)) {
	// Not a possible signature, so the first pass always loads — even with no
	// files present, the caller then holds an empty DB rather than nil.
	last := "\x00"
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if sig := signature(dir); sig != last {
			start := time.Now()
			db, err := Load(dir)
			if err != nil {
				log.Warn("flow geo data not loaded", "dir", dir, "err", err)
			} else {
				last = sig
				asn, cc := db.Loaded()
				if sig != "" {
					log.Info("flow geo data loaded", "dir", dir, "asn_ranges", asn,
						"country_ranges", cc, "skipped", db.SkippedASN+db.SkippedCountry,
						"took", time.Since(start).Round(time.Millisecond).String())
				}
				set(db)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// signature summarises the files present, so a change to any is noticed.
func signature(dir string) string {
	var b strings.Builder
	for _, name := range []string{ASNFile, CountryFile} {
		if p, ok := find(dir, name); ok {
			if st, err := os.Stat(p); err == nil {
				fmt.Fprintf(&b, "%s:%d:%d;", p, st.Size(), st.ModTime().UnixNano())
			}
		}
	}
	return b.String()
}
