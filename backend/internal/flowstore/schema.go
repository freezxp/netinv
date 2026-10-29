package flowstore

import (
	"context"
	"fmt"
	"strings"
)

// Retention is how long each table keeps rows, in days. ClickHouse enforces
// it with TTL, so retention cannot silently stop the way a pruning job can.
type Retention struct {
	RawDays    int
	RollupDays int
}

// DefaultRetention is ADR-026's: a month of raw flows for forensics, a year
// of hourly rollups for long-range charts.
var DefaultRetention = Retention{RawDays: 30, RollupDays: 365}

// rollupKey is everything the hourly rollup keeps apart. It is also the
// table's sorting key, which SummingMergeTree requires: a column outside the
// key would be summed away into an arbitrary survivor. What is missing is
// deliberate — MAC, NAT, user, TCP flags, next hop and ephemeral ports make
// nearly every flow unique, and keeping them would make the rollup as large
// as the raw table it exists to outlive.
var rollupKey = []string{
	"exporter", "hour", "in_if", "out_if", "src_addr", "dst_addr", "proto",
	"src_port", "dst_port", "tos", "src_as", "dst_as", "src_vlan", "dst_vlan",
	"direction", "app_id", "type", "src_country", "dst_country",
	"src_as_name", "dst_as_name", "app_name",
}

// EphemeralPort is where the rollup starts recording a port as 0. 32768 is
// the bottom of Linux's ephemeral range; Windows starts at 49152, so this
// errs towards keeping registered ports, which is the useful direction.
const EphemeralPort = 32768

func schema(r Retention) []string {
	return []string{
		`CREATE DATABASE IF NOT EXISTS ` + Database,

		// One row per decoded flow. Ordered by exporter then time, because
		// nearly every question is scoped to a device and a window, and the
		// partition per day lets TTL drop whole parts instead of rewriting
		// them.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.flows (
			received     DateTime64(3, 'UTC'),
			time_start   DateTime64(3, 'UTC'),
			time_end     DateTime64(3, 'UTC'),
			exporter     IPv6,
			type         LowCardinality(String),
			sampling     UInt32,
			in_if        UInt32,
			out_if       UInt32,
			src_addr     IPv6,
			dst_addr     IPv6,
			src_port     UInt16,
			dst_port     UInt16,
			proto        UInt8,
			tos          UInt8,
			tcp_flags    UInt8,
			src_as       UInt32,
			dst_as       UInt32,
			src_as_name  LowCardinality(String),
			dst_as_name  LowCardinality(String),
			src_country  LowCardinality(String),
			dst_country  LowCardinality(String),
			src_mask     UInt8,
			dst_mask     UInt8,
			src_mac      UInt64,
			dst_mac      UInt64,
			src_vlan     UInt16,
			dst_vlan     UInt16,
			next_hop     IPv6,
			bgp_next_hop IPv6,
			direction    UInt8,
			icmp_type    UInt16,
			app_id       LowCardinality(String),
			app_name     LowCardinality(String),
			nat_src_addr IPv6,
			nat_dst_addr IPv6,
			nat_src_port UInt16,
			nat_dst_port UInt16,
			fw_event     UInt8,
			user         LowCardinality(String),
			bytes        UInt64,
			packets      UInt64
		) ENGINE = MergeTree
		PARTITION BY toYYYYMMDD(received)
		ORDER BY (exporter, received)
		TTL toDateTime(received) + INTERVAL %d DAY
		-- The deduplication window lets a retried batch (same token) be
		-- recognised and skipped; flows_1h carries one too, or the retry would
		-- be dropped here and counted twice in the rollup.
		SETTINGS ttl_only_drop_parts = 1, non_replicated_deduplication_window = 1000`,
			Database, r.RawDays),

		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.flows_1h (
			hour        DateTime('UTC'),
			exporter    IPv6,
			type        LowCardinality(String),
			in_if       UInt32,
			out_if      UInt32,
			src_addr    IPv6,
			dst_addr    IPv6,
			src_port    UInt16,
			dst_port    UInt16,
			proto       UInt8,
			tos         UInt8,
			src_as      UInt32,
			dst_as      UInt32,
			src_as_name LowCardinality(String),
			dst_as_name LowCardinality(String),
			src_country LowCardinality(String),
			dst_country LowCardinality(String),
			src_vlan    UInt16,
			dst_vlan    UInt16,
			direction   UInt8,
			app_id      LowCardinality(String),
			app_name    LowCardinality(String),
			bytes       UInt64,
			packets     UInt64,
			flows       UInt64
		) ENGINE = SummingMergeTree((bytes, packets, flows))
		PARTITION BY toYYYYMM(hour)
		ORDER BY (%s)
		TTL hour + INTERVAL %d DAY
		SETTINGS non_replicated_deduplication_window = 1000`,
			Database, strings.Join(rollupKey, ", "), r.RollupDays),

		// The view rolls each inserted block up as it lands. Rows for the same
		// key arriving in different blocks are summed later by merges, so
		// queries on the rollup must still sum(), never read a row as final.
		fmt.Sprintf(`CREATE MATERIALIZED VIEW IF NOT EXISTS %s.flows_1h_mv TO %s.flows_1h AS
		SELECT
			toStartOfHour(received) AS hour, exporter, type, in_if, out_if,
			src_addr, dst_addr,
			if(src_port >= %d, 0, src_port) AS src_port,
			if(dst_port >= %d, 0, dst_port) AS dst_port,
			proto, tos, src_as, dst_as, src_as_name, dst_as_name,
			src_country, dst_country, src_vlan, dst_vlan, direction,
			app_id, app_name,
			sum(bytes) AS bytes, sum(packets) AS packets, count() AS flows
		FROM %s.flows
		GROUP BY hour, exporter, type, in_if, out_if, src_addr, dst_addr,
			src_port, dst_port, proto, tos, src_as, dst_as, src_as_name,
			dst_as_name, src_country, dst_country, src_vlan, dst_vlan,
			direction, app_id, app_name`,
			Database, Database, EphemeralPort, EphemeralPort, Database),

		// What the collector reports about itself, read by the API's status
		// endpoint. The collector has no database of its own to write this
		// to, and ClickHouse is what the reader is already talking to.
		`CREATE TABLE IF NOT EXISTS ` + Database + `.collector_status (
			at          DateTime('UTC'),
			written     UInt64,
			dropped     UInt64,
			failed      UInt64,
			queued      UInt64,
			geo_asn     UInt32,
			geo_country UInt32
		) ENGINE = ReplacingMergeTree(at)
		ORDER BY tuple()
		TTL at + INTERVAL 7 DAY`,
	}
}

// EnsureSchema creates the tables if they are missing and brings their TTL
// in line with r. It is idempotent and runs at every collector start, which
// is what makes NETINV_FLOW_RAW_RETENTION take effect on a restart.
func EnsureSchema(ctx context.Context, c *Client, r Retention) error {
	if r.RawDays <= 0 || r.RollupDays <= 0 {
		return fmt.Errorf("flowstore: retention must be positive (raw %d, rollup %d days)", r.RawDays, r.RollupDays)
	}
	for _, stmt := range schema(r) {
		if err := c.Exec(ctx, stmt, nil); err != nil {
			return fmt.Errorf("flowstore: schema: %w", err)
		}
	}
	// CREATE IF NOT EXISTS leaves an existing table's TTL alone, so a changed
	// setting is applied here — and only when it differs, because MODIFY TTL
	// is a mutation and need not run on every start.
	for table, ttl := range map[string]string{
		"flows":    fmt.Sprintf("toDateTime(received) + toIntervalDay(%d)", r.RawDays),
		"flows_1h": fmt.Sprintf("hour + toIntervalDay(%d)", r.RollupDays),
	} {
		rows, err := c.Select(ctx, `SELECT create_table_query AS q FROM system.tables
			WHERE database = {db:String} AND name = {t:String}`,
			map[string]string{"db": Database, "t": table})
		if err != nil {
			return fmt.Errorf("flowstore: read %s ttl: %w", table, err)
		}
		if len(rows) == 1 && strings.Contains(fmt.Sprint(rows[0]["q"]), "TTL "+ttl) {
			continue
		}
		expr := strings.Replace(ttl, "toIntervalDay(", "INTERVAL ", 1)
		expr = strings.TrimSuffix(expr, ")") + " DAY"
		if err := c.Exec(ctx, fmt.Sprintf("ALTER TABLE %s.%s MODIFY TTL %s", Database, table, expr), nil); err != nil {
			return fmt.Errorf("flowstore: set %s ttl: %w", table, err)
		}
	}
	return nil
}
