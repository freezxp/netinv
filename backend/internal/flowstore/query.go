package flowstore

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// Field is one column the explorer can group or filter by. Only names in
// Fields reach SQL; a request naming anything else is refused, which is what
// keeps a group-by list from being a way to write SQL.
type Field struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	// Kind tells the UI how to render and edit values: addr, port, number,
	// text, country, mac, proto, iface, flags, as, dscp, direction, exporter.
	Kind string `json:"kind"`
	// Rollup is true when the hourly table keeps the field, i.e. when it can
	// be used past the raw retention.
	Rollup bool `json:"rollup"`
	// Group is false for fields that can only filter.
	Group bool `json:"group"`
	// col is the stored column (or expression over columns) it reads.
	col string
	// any lists the columns a "src or dst" virtual field ORs over.
	any []string
	// name is the column holding a display name for an AS number.
	name string
}

func addrOut(c string) string { return "replaceRegexpOne(toString(" + c + "), '^::ffff:', '')" }

// Fields is the explorer's whole vocabulary, in the order the UI offers it.
var Fields = []Field{
	{Name: "exporter", Label: "Exporter", Kind: "exporter", Rollup: true, Group: true, col: "exporter"},
	{Name: "in_if", Label: "Input interface", Kind: "iface", Rollup: true, Group: true, col: "in_if"},
	{Name: "out_if", Label: "Output interface", Kind: "iface", Rollup: true, Group: true, col: "out_if"},
	{Name: "src_addr", Label: "Source address", Kind: "addr", Rollup: true, Group: true, col: "src_addr"},
	{Name: "dst_addr", Label: "Destination address", Kind: "addr", Rollup: true, Group: true, col: "dst_addr"},
	{Name: "src_port", Label: "Source port", Kind: "port", Rollup: true, Group: true, col: "src_port"},
	{Name: "dst_port", Label: "Destination port", Kind: "port", Rollup: true, Group: true, col: "dst_port"},
	{Name: "proto", Label: "Protocol", Kind: "proto", Rollup: true, Group: true, col: "proto"},
	{Name: "dscp", Label: "DSCP", Kind: "dscp", Rollup: true, Group: true, col: "bitShiftRight(tos, 2)"},
	{Name: "src_as", Label: "Source AS", Kind: "as", Rollup: true, Group: true, col: "src_as", name: "src_as_name"},
	{Name: "dst_as", Label: "Destination AS", Kind: "as", Rollup: true, Group: true, col: "dst_as", name: "dst_as_name"},
	{Name: "src_country", Label: "Source country", Kind: "country", Rollup: true, Group: true, col: "src_country"},
	{Name: "dst_country", Label: "Destination country", Kind: "country", Rollup: true, Group: true, col: "dst_country"},
	{Name: "src_vlan", Label: "Source VLAN", Kind: "number", Rollup: true, Group: true, col: "src_vlan"},
	{Name: "dst_vlan", Label: "Destination VLAN", Kind: "number", Rollup: true, Group: true, col: "dst_vlan"},
	{Name: "direction", Label: "Direction", Kind: "direction", Rollup: true, Group: true, col: "direction"},
	{Name: "app_id", Label: "Application ID", Kind: "text", Rollup: true, Group: true, col: "app_id"},
	{Name: "app_name", Label: "Application", Kind: "text", Rollup: true, Group: true, col: "app_name"},
	{Name: "type", Label: "Export format", Kind: "text", Rollup: true, Group: true, col: "type"},
	{Name: "tcp_flags", Label: "TCP flags", Kind: "flags", Group: true, col: "tcp_flags"},
	{Name: "src_mac", Label: "Source MAC", Kind: "mac", Group: true, col: "src_mac"},
	{Name: "dst_mac", Label: "Destination MAC", Kind: "mac", Group: true, col: "dst_mac"},
	{Name: "next_hop", Label: "Next hop", Kind: "addr", Group: true, col: "next_hop"},
	{Name: "icmp_type", Label: "ICMP type/code", Kind: "number", Group: true, col: "icmp_type"},
	{Name: "nat_src_addr", Label: "NAT source address", Kind: "addr", Group: true, col: "nat_src_addr"},
	{Name: "nat_dst_addr", Label: "NAT destination address", Kind: "addr", Group: true, col: "nat_dst_addr"},
	{Name: "nat_src_port", Label: "NAT source port", Kind: "port", Group: true, col: "nat_src_port"},
	{Name: "nat_dst_port", Label: "NAT destination port", Kind: "port", Group: true, col: "nat_dst_port"},
	{Name: "fw_event", Label: "Firewall event", Kind: "number", Group: true, col: "fw_event"},
	{Name: "user", Label: "User", Kind: "text", Group: true, col: "user"},
	// Either-end filters: "this address, whichever side it is on".
	{Name: "addr", Label: "Address (either end)", Kind: "addr", Rollup: true, any: []string{"src_addr", "dst_addr"}},
	{Name: "port", Label: "Port (either end)", Kind: "port", Rollup: true, any: []string{"src_port", "dst_port"}},
	{Name: "as", Label: "AS (either end)", Kind: "as", Rollup: true, any: []string{"src_as", "dst_as"}},
	{Name: "country", Label: "Country (either end)", Kind: "country", Rollup: true, any: []string{"src_country", "dst_country"}},
	{Name: "vlan", Label: "VLAN (either end)", Kind: "number", Rollup: true, any: []string{"src_vlan", "dst_vlan"}},
}

var fieldByName = func() map[string]Field {
	m := map[string]Field{}
	for _, f := range Fields {
		m[f.Name] = f
	}
	return m
}()

// LookupField returns a field by name.
func LookupField(name string) (Field, bool) {
	f, ok := fieldByName[name]
	return f, ok
}

// Filter is one condition. Values are ORed; Negate turns the whole condition
// into "none of these". An address value may be a CIDR prefix.
type Filter struct {
	Field  string   `json:"field"`
	Negate bool     `json:"negate,omitempty"`
	Values []string `json:"values"`
}

// Query is an explorer request after the API has resolved devices to
// exporter addresses.
type Query struct {
	From, To time.Time
	Filters  []Filter
	GroupBy  []string
	// Metric orders and charts by bytes, packets or flows.
	Metric string
	Limit  int
	// Exporters, when non-nil, restricts to these addresses (a device or
	// site filter, resolved by the caller). Empty but non-nil matches nothing.
	Exporters []netip.Addr
	// RawCutoff is the oldest instant the raw table still holds.
	RawCutoff time.Time
}

// Table names the store a query will read.
type Table string

const (
	TableRaw    Table = "flows"
	TableRollup Table = "flows_1h"
)

// QueryError is a request the store cannot answer as asked; its message is
// meant for the user.
type QueryError struct{ Msg string }

func (e *QueryError) Error() string { return e.Msg }

func bad(format string, a ...any) error { return &QueryError{Msg: fmt.Sprintf(format, a...)} }

// MaxGroupBy, MaxFilters and MaxLimit bound a request's cost.
const (
	MaxGroupBy = 4
	MaxFilters = 20
	MaxValues  = 100
	MaxLimit   = 100
)

// built is a compiled query: SQL fragments plus their parameters.
type built struct {
	table  Table
	tcol   string
	where  string
	keys   []string // display expressions, aliased k0..kn
	gkeys  []string // grouping expressions, aliased g0..gn (no aggregates)
	params map[string]string
	metric string // sum expression for the ordering metric
	count  string // flow-count expression
}

// chooseTable reads the rollup when the window starts before the raw data
// does, or when it is long and every field asked for is in the rollup — a
// month of raw flows is far more rows than a month of hours.
func chooseTable(q *Query) (Table, error) {
	needRaw := ""
	for _, n := range q.GroupBy {
		if f := fieldByName[n]; !f.Rollup {
			needRaw = f.Label
		}
	}
	for _, fl := range q.Filters {
		if f := fieldByName[fl.Field]; !f.Rollup {
			needRaw = f.Label
		}
	}
	if !q.RawCutoff.IsZero() && q.From.Before(q.RawCutoff) {
		if needRaw != "" {
			return "", bad("%s is kept only in raw flows, which start at %s; narrow the time range or drop it",
				needRaw, q.RawCutoff.UTC().Format(time.RFC3339))
		}
		return TableRollup, nil
	}
	if needRaw == "" && q.To.Sub(q.From) > 7*24*time.Hour {
		return TableRollup, nil
	}
	return TableRaw, nil
}

func compile(q *Query) (*built, error) {
	if !q.To.After(q.From) {
		return nil, bad("'to' must be after 'from'")
	}
	if len(q.GroupBy) > MaxGroupBy {
		return nil, bad("at most %d group-by fields", MaxGroupBy)
	}
	if len(q.Filters) > MaxFilters {
		return nil, bad("at most %d filters", MaxFilters)
	}
	for _, n := range q.GroupBy {
		f, ok := fieldByName[n]
		if !ok || !f.Group {
			return nil, bad("cannot group by %q", n)
		}
	}
	table, err := chooseTable(q)
	if err != nil {
		return nil, err
	}
	b := &built{table: table, params: map[string]string{}}
	if table == TableRollup {
		b.tcol, b.count = "hour", "sum(flows)"
	} else {
		b.tcol, b.count = "received", "count()"
	}
	switch q.Metric {
	case "", "bytes":
		b.metric = "sum(bytes)"
	case "packets":
		b.metric = "sum(packets)"
	case "flows":
		b.metric = b.count
	default:
		return nil, bad("metric must be bytes, packets or flows")
	}

	conds := []string{}
	if table == TableRollup {
		// The rollup is hourly: widen to whole hours so the first and last
		// partial hours are included rather than silently dropped.
		conds = append(conds, "hour >= toStartOfHour({from:DateTime('UTC')}) AND hour < {to:DateTime('UTC')}")
		b.params["from"] = q.From.UTC().Format("2006-01-02 15:04:05")
		b.params["to"] = q.To.UTC().Format("2006-01-02 15:04:05")
	} else {
		conds = append(conds, "received >= {from:DateTime64(3, 'UTC')} AND received < {to:DateTime64(3, 'UTC')}")
		b.params["from"] = q.From.UTC().Format(chTime)
		b.params["to"] = q.To.UTC().Format(chTime)
	}
	if q.Exporters != nil {
		if len(q.Exporters) == 0 {
			conds = append(conds, "0")
		} else {
			ph := make([]string, 0, len(q.Exporters))
			for i, a := range q.Exporters {
				k := "e" + strconv.Itoa(i)
				b.params[k] = chAddr(a)
				ph = append(ph, "{"+k+":IPv6}")
			}
			conds = append(conds, "exporter IN ("+strings.Join(ph, ", ")+")")
		}
	}
	for i, fl := range q.Filters {
		c, err := b.filter(i, fl)
		if err != nil {
			return nil, err
		}
		conds = append(conds, c)
	}
	b.where = strings.Join(conds, " AND ")

	for i, n := range q.GroupBy {
		f := fieldByName[n]
		b.keys = append(b.keys, outExpr(f)+" AS k"+strconv.Itoa(i))
		b.gkeys = append(b.gkeys, groupExpr(f)+" AS g"+strconv.Itoa(i))
	}
	return b, nil
}

// groupExpr is what a field groups on: the stored value, never a display
// form that needs an aggregate (an AS name is any() over its rows).
func groupExpr(f Field) string {
	if f.Kind == "as" {
		return f.col
	}
	return outExpr(f)
}

// outExpr renders a field for display: addresses without the IPv4-mapped
// prefix, MACs in colon form, everything else as text so one JSON shape
// carries every kind.
func outExpr(f Field) string {
	switch f.Kind {
	case "addr", "exporter":
		return addrOut(f.col)
	case "mac":
		return "MACNumToString(" + f.col + ")"
	case "iface":
		// An ifIndex only identifies an interface within one exporter, so
		// interface 3 of two routers must not be summed into one row. The key
		// carries its exporter: "192.0.2.1|3".
		return "concat(" + addrOut("exporter") + ", '|', toString(" + f.col + "))"
	case "as":
		// "13335 CLOUDFLARENET": the number first, so a value clicked into a
		// filter still parses, and the name — the part a person recognises —
		// after it. any() because rows of one AS share one name.
		if f.name != "" {
			return "concat(toString(" + f.col + "), if(any(" + f.name + ") = '', '', concat(' ', any(" + f.name + "))))"
		}
		return "toString(" + f.col + ")"
	default:
		return "toString(" + f.col + ")"
	}
}

func (b *built) filter(i int, fl Filter) (string, error) {
	f, ok := fieldByName[fl.Field]
	if !ok {
		return "", bad("cannot filter by %q", fl.Field)
	}
	if len(fl.Values) == 0 {
		return "", bad("filter on %s has no value", f.Label)
	}
	if len(fl.Values) > MaxValues {
		return "", bad("at most %d values per filter", MaxValues)
	}
	cols := f.any
	if cols == nil {
		cols = []string{f.col}
	}
	var ors []string
	for j, v := range fl.Values {
		v = strings.TrimSpace(v)
		k := fmt.Sprintf("f%d_%d", i, j)
		for ci, col := range cols {
			kk := k
			if len(cols) > 1 {
				kk = fmt.Sprintf("%s_%d", k, ci)
			}
			c, err := b.cond(f, col, kk, v)
			if err != nil {
				return "", err
			}
			ors = append(ors, c)
		}
	}
	expr := "(" + strings.Join(ors, " OR ") + ")"
	if fl.Negate {
		expr = "NOT " + expr
	}
	return expr, nil
}

// cond compiles one value against one column, as a typed parameter.
func (b *built) cond(f Field, col, k, v string) (string, error) {
	switch f.Kind {
	case "addr", "exporter":
		if strings.Contains(v, "/") {
			p, err := netip.ParsePrefix(v)
			if err != nil {
				return "", bad("%s: %q is not an address or prefix", f.Label, v)
			}
			lo, hi := prefixRange(p.Masked())
			b.params[k+"lo"], b.params[k+"hi"] = lo, hi
			return fmt.Sprintf("%s BETWEEN {%slo:IPv6} AND {%shi:IPv6}", col, k, k), nil
		}
		a, err := netip.ParseAddr(v)
		if err != nil {
			return "", bad("%s: %q is not an address or prefix", f.Label, v)
		}
		b.params[k] = chAddr(a)
		return fmt.Sprintf("%s = {%s:IPv6}", col, k), nil
	case "mac":
		b.params[k] = v
		return fmt.Sprintf("%s = MACStringToNum({%s:String})", col, k), nil
	case "iface":
		// "address|ifIndex" names one interface of one exporter; a bare
		// number matches that ifIndex on every exporter.
		if addr, idx, ok := strings.Cut(v, "|"); ok {
			a, err := netip.ParseAddr(addr)
			n, err2 := strconv.ParseUint(idx, 10, 32)
			if err != nil || err2 != nil {
				return "", bad("%s: %q is not address|ifIndex", f.Label, v)
			}
			b.params[k+"e"], b.params[k] = chAddr(a), strconv.FormatUint(n, 10)
			return fmt.Sprintf("(exporter = {%se:IPv6} AND %s = {%s:UInt64})", k, col, k), nil
		}
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return "", bad("%s: %q is not an ifIndex", f.Label, v)
		}
		b.params[k] = strconv.FormatUint(n, 10)
		return fmt.Sprintf("%s = {%s:UInt64}", col, k), nil
	case "text", "country":
		if f.Kind == "country" {
			v = strings.ToUpper(v)
		}
		b.params[k] = v
		return fmt.Sprintf("%s = {%s:String}", col, k), nil
	case "direction":
		switch strings.ToLower(v) {
		case "ingress", "1":
			v = "1"
		case "egress", "2":
			v = "2"
		case "unknown", "0":
			v = "0"
		default:
			return "", bad("direction must be ingress, egress or unknown")
		}
		fallthrough
	default:
		n, err := strconv.ParseUint(protoNumber(f, v), 10, 64)
		if err != nil {
			return "", bad("%s: %q is not a number", f.Label, v)
		}
		b.params[k] = strconv.FormatUint(n, 10)
		return fmt.Sprintf("%s = {%s:UInt64}", col, k), nil
	}
}

// protoNumber lets a protocol filter be written by name.
func protoNumber(f Field, v string) string {
	if f.Kind != "proto" {
		return v
	}
	switch strings.ToLower(v) {
	case "icmp":
		return "1"
	case "tcp":
		return "6"
	case "udp":
		return "17"
	case "gre":
		return "47"
	case "esp":
		return "50"
	case "icmpv6", "ipv6-icmp":
		return "58"
	case "ospf":
		return "89"
	case "sctp":
		return "132"
	}
	return v
}

// prefixRange returns the first and last address of a prefix as IPv6 text,
// IPv4 prefixes mapped — the form the IPv6 columns store.
func prefixRange(p netip.Prefix) (string, string) {
	a := p.Addr()
	bits := p.Bits()
	if a.Is4() {
		a = netip.AddrFrom16(a.As16())
		bits += 96
	}
	lo := a.As16()
	hi := lo
	for i := bits; i < 128; i++ {
		hi[i/8] |= 0x80 >> (i % 8)
	}
	return netip.AddrFrom16(lo).String(), netip.AddrFrom16(hi).String()
}

// Group is one row of a top-N result.
type Group struct {
	Keys    []string `json:"keys"`
	Bytes   uint64   `json:"bytes"`
	Packets uint64   `json:"packets"`
	Flows   uint64   `json:"flows"`
}

// Series is the top groups over time, plus the total, at one step.
type Series struct {
	Step  int64       `json:"step"`
	Times []int64     `json:"times"`
	Total []float64   `json:"total"`
	Lines [][]float64 `json:"lines"`
}

// Result answers an explorer query.
type Result struct {
	Table  Table   `json:"table"`
	Groups []Group `json:"groups"`
	Total  Group   `json:"total"`
	Series Series  `json:"series"`
}

// Step picks the chart resolution: about 150 points across the window, on a
// round interval, never finer than the table holds.
func Step(table Table, from, to time.Time) time.Duration {
	steps := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute,
		time.Hour, 3 * time.Hour, 6 * time.Hour, 24 * time.Hour}
	want := to.Sub(from) / 150
	for _, s := range steps {
		if table == TableRollup && s < time.Hour {
			continue
		}
		if s >= want {
			return s
		}
	}
	return steps[len(steps)-1]
}

// Explore runs a top-N query and its time series.
func Explore(ctx context.Context, c *Client, q *Query) (*Result, error) {
	b, err := compile(q)
	if err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 || limit > MaxLimit {
		limit = 10
	}
	from := Database + "." + string(b.table)
	res := &Result{Table: b.table, Groups: []Group{}}

	tot, err := c.Select(ctx, fmt.Sprintf(
		"SELECT sum(bytes) AS m_bytes, sum(packets) AS m_packets, %s AS m_flows FROM %s WHERE %s",
		b.count, from, b.where), b.params)
	if err != nil {
		return nil, err
	}
	if len(tot) == 1 {
		res.Total = Group{Bytes: parseUint(tot[0]["m_bytes"]), Packets: parseUint(tot[0]["m_packets"]), Flows: parseUint(tot[0]["m_flows"])}
	}

	gAliases := make([]string, len(b.gkeys))
	kAliases := make([]string, len(b.keys))
	for i := range b.keys {
		gAliases[i] = "g" + strconv.Itoa(i)
		kAliases[i] = "k" + strconv.Itoa(i)
	}
	// gkeys index each top group by its grouping values, which is what the
	// series rows come back keyed by.
	groupIdx := map[string]int{}
	topSQL := ""
	if len(b.keys) > 0 {
		topSQL = fmt.Sprintf(
			"SELECT %s, %s, sum(bytes) AS m_bytes, sum(packets) AS m_packets, %s AS m_flows FROM %s WHERE %s GROUP BY %s ORDER BY %s DESC LIMIT %d",
			strings.Join(b.gkeys, ", "), strings.Join(b.keys, ", "), b.count, from, b.where,
			strings.Join(gAliases, ", "), orderAlias(q.Metric), limit)
		rows, err := c.Select(ctx, topSQL, b.params)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			g := Group{Bytes: parseUint(r["m_bytes"]), Packets: parseUint(r["m_packets"]), Flows: parseUint(r["m_flows"])}
			gk := make([]string, len(gAliases))
			for i := range gAliases {
				g.Keys = append(g.Keys, fmt.Sprint(r[kAliases[i]]))
				gk[i] = fmt.Sprint(r[gAliases[i]])
			}
			groupIdx[strings.Join(gk, "\x00")] = len(res.Groups)
			res.Groups = append(res.Groups, g)
		}
	}

	step := Step(b.table, q.From, q.To)
	res.Series.Step = int64(step / time.Second)
	bucket := fmt.Sprintf("toUnixTimestamp(toStartOfInterval(%s, INTERVAL %d SECOND)) AS t", b.tcol, res.Series.Step)
	// Totals per bucket first; it fixes the time axis every line shares.
	trows, err := c.Select(ctx, fmt.Sprintf("SELECT %s, %s AS m_v FROM %s WHERE %s GROUP BY t ORDER BY t",
		bucket, b.metric, from, b.where), b.params)
	if err != nil {
		return nil, err
	}
	index := map[int64]int{}
	for _, r := range trows {
		t := int64(parseUint(r["t"])) //nolint:gosec // a Unix time
		index[t] = len(res.Series.Times)
		res.Series.Times = append(res.Series.Times, t)
		res.Series.Total = append(res.Series.Total, rate(parseUint(r["m_v"]), q.Metric, step))
	}
	if len(res.Groups) > 0 {
		for range res.Groups {
			res.Series.Lines = append(res.Series.Lines, make([]float64, len(res.Series.Times)))
		}
		// The same top-N selection, repeated as a subquery so ClickHouse
		// picks the groups rather than this code passing them back as
		// parameters.
		srows, err := c.Select(ctx, fmt.Sprintf(
			"SELECT %s, %s, %s AS m_v FROM %s WHERE %s AND (%s) IN (SELECT %s FROM (%s)) GROUP BY t, %s",
			bucket, strings.Join(b.gkeys, ", "), b.metric, from, b.where,
			tupleOf(b.gkeys), strings.Join(gAliases, ", "), topSQL, strings.Join(gAliases, ", ")), b.params)
		if err != nil {
			return nil, err
		}
		for _, r := range srows {
			gk := make([]string, len(gAliases))
			for i, a := range gAliases {
				gk[i] = fmt.Sprint(r[a])
			}
			gi, ok := groupIdx[strings.Join(gk, "\x00")]
			ti, ok2 := index[int64(parseUint(r["t"]))] //nolint:gosec // a Unix time
			if ok && ok2 {
				res.Series.Lines[gi][ti] = rate(parseUint(r["m_v"]), q.Metric, step)
			}
		}
	}
	return res, nil
}

// tupleOf turns "expr AS k0, expr AS k1" back into "(expr, expr)" for the IN
// test, which must compare the computed values, not the aliases.
func tupleOf(keys []string) string {
	exprs := make([]string, len(keys))
	for i, k := range keys {
		exprs[i] = k[:strings.LastIndex(k, " AS ")]
	}
	return strings.Join(exprs, ", ")
}

func orderAlias(metric string) string {
	// Aggregates carry an m_ prefix so no alias can collide with a column.
	switch metric {
	case "packets":
		return "m_packets"
	case "flows":
		return "m_flows"
	}
	return "m_bytes"
}

// rate turns a bucket's sum into a per-second figure: bits/s for bytes.
func rate(v uint64, metric string, step time.Duration) float64 {
	per := float64(v) / step.Seconds()
	if metric == "" || metric == "bytes" {
		return per * 8
	}
	return per
}

// RecordsQuery asks for individual flows, newest first or largest first.
type RecordsQuery struct {
	Query
	OrderBy string // time or bytes
}

// Records returns raw flows. Raw only: the rollup has no individual flows.
func Records(ctx context.Context, c *Client, q *RecordsQuery) ([]map[string]any, error) {
	if !q.RawCutoff.IsZero() && q.To.Before(q.RawCutoff) {
		return nil, bad("individual flows are kept for the raw retention only; nothing before %s",
			q.RawCutoff.UTC().Format(time.RFC3339))
	}
	qq := q.Query
	qq.GroupBy = nil
	if !qq.RawCutoff.IsZero() && qq.From.Before(qq.RawCutoff) {
		qq.From = qq.RawCutoff
	}
	qq.RawCutoff = time.Time{}
	// compile with a short window so chooseTable keeps the raw table; the
	// real bounds are restored in the parameters below.
	short := qq
	short.To = short.From.Add(time.Hour)
	b, err := compile(&short)
	if err != nil {
		return nil, err
	}
	b.params["to"] = qq.To.UTC().Format(chTime)
	limit := q.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	order := "received DESC"
	if q.OrderBy == "bytes" {
		order = "bytes DESC"
	}
	cols := []string{
		"toUnixTimestamp64Milli(received) AS received",
		"toUnixTimestamp64Milli(time_start) AS time_start",
		"toUnixTimestamp64Milli(time_end) AS time_end",
		addrOut("exporter") + " AS exporter", "type", "sampling", "in_if", "out_if",
		addrOut("src_addr") + " AS src_addr", addrOut("dst_addr") + " AS dst_addr",
		"src_port", "dst_port", "proto", "bitShiftRight(tos, 2) AS dscp", "tcp_flags",
		"src_as", "dst_as", "src_as_name", "dst_as_name", "src_country", "dst_country",
		"MACNumToString(src_mac) AS src_mac", "MACNumToString(dst_mac) AS dst_mac",
		"src_vlan", "dst_vlan", addrOut("next_hop") + " AS next_hop", "direction", "icmp_type",
		"app_id", "app_name", addrOut("nat_src_addr") + " AS nat_src_addr",
		addrOut("nat_dst_addr") + " AS nat_dst_addr", "nat_src_port", "nat_dst_port",
		"fw_event", "user", "bytes", "packets",
	}
	return c.Select(ctx, fmt.Sprintf("SELECT %s FROM %s.flows WHERE %s ORDER BY %s LIMIT %d",
		strings.Join(cols, ", "), Database, b.where, order, limit), b.params)
}

// Status describes the store for the UI: whether it holds anything, how far
// back, how big, and what the collector last reported.
type Status struct {
	Rows        uint64     `json:"rows"`
	BytesOnDisk uint64     `json:"bytes_on_disk"`
	Oldest      *time.Time `json:"oldest,omitempty"`
	Newest      *time.Time `json:"newest,omitempty"`
	RollupRows  uint64     `json:"rollup_rows"`
	Written     uint64     `json:"written"`
	Dropped     uint64     `json:"dropped"`
	Queued      uint64     `json:"queued"`
	GeoASN      uint64     `json:"geo_asn_ranges"`
	GeoCountry  uint64     `json:"geo_country_ranges"`
	ReportedAt  *time.Time `json:"reported_at,omitempty"`
}

// ReadStatus gathers Status from system tables and collector_status.
func ReadStatus(ctx context.Context, c *Client) (*Status, error) {
	st := &Status{}
	parts, err := c.Select(ctx, `SELECT table, sum(rows) AS rows, sum(bytes_on_disk) AS bytes
		FROM system.parts WHERE database = {db:String} AND active GROUP BY table`,
		map[string]string{"db": Database})
	if err != nil {
		return nil, err
	}
	for _, p := range parts {
		switch p["table"] {
		case "flows":
			st.Rows = parseUint(p["rows"])
		case "flows_1h":
			st.RollupRows = parseUint(p["rows"])
		}
		st.BytesOnDisk += parseUint(p["bytes"])
	}
	if st.Rows > 0 {
		r, err := c.Select(ctx, "SELECT toUnixTimestamp(min(received)) AS lo, toUnixTimestamp(max(received)) AS hi FROM "+Database+".flows", nil)
		if err == nil && len(r) == 1 {
			lo := time.Unix(int64(parseUint(r[0]["lo"])), 0).UTC() //nolint:gosec // a Unix time
			hi := time.Unix(int64(parseUint(r[0]["hi"])), 0).UTC() //nolint:gosec // a Unix time
			st.Oldest, st.Newest = &lo, &hi
		}
	}
	r, err := c.Select(ctx, `SELECT toUnixTimestamp(at) AS at, written, dropped, queued, geo_asn, geo_country
		FROM `+Database+`.collector_status ORDER BY at DESC LIMIT 1`, nil)
	if err == nil && len(r) == 1 {
		at := time.Unix(int64(parseUint(r[0]["at"])), 0).UTC() //nolint:gosec // a Unix time
		st.ReportedAt = &at
		st.Written, st.Dropped, st.Queued = parseUint(r[0]["written"]), parseUint(r[0]["dropped"]), parseUint(r[0]["queued"])
		st.GeoASN, st.GeoCountry = parseUint(r[0]["geo_asn"]), parseUint(r[0]["geo_country"])
	}
	return st, nil
}
