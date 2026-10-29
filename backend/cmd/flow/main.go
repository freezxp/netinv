// netinv-flow — flow export collector (ADR-020, ADR-026, doc 34).
//
// Receives NetFlow v5/v9, IPFIX and sFlow, aggregates to top-N per interface
// and writes bounded-cardinality series to VictoriaMetrics. When a ClickHouse
// URL is configured it also stores every flow, enriched with country and AS
// name where the offline databases are present.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/freezxp/netinv/backend/internal/flow"
	"github.com/freezxp/netinv/backend/internal/flow/geo"
	"github.com/freezxp/netinv/backend/internal/flowstore"
	"github.com/freezxp/netinv/backend/internal/platform/service"
	"github.com/freezxp/netinv/backend/internal/platform/vmwrite"
)

func main() {
	service.Run("flow", func(ctx context.Context, rt *service.Runtime) error {
		vmURL := os.Getenv("NETINV_VM_URL")
		if vmURL == "" {
			rt.Log.Warn("NETINV_VM_URL not set — idle skeleton mode")
			rt.Health.SetReady(true)
			<-ctx.Done()
			return nil
		}
		addr := os.Getenv("NETINV_FLOW_ADDR")
		if addr == "" {
			// The conventional ports: NetFlow on 2055, IPFIX on 4739, sFlow on
			// 6343. An exporter that cannot be told which to use will use its
			// own convention, and the format is read from the datagram either
			// way.
			addr = ":2055,:4739,:6343"
		}
		agg := flow.NewAggregator()
		// How many buckets each interface keeps per dimension per interval.
		// Configurable because the right answer depends on fleet size: deeper
		// tables cost series, and a large fleet reaches NFR-03's ceiling
		// before a small one does (doc 34 §1).
		if v := os.Getenv("NETINV_FLOW_TOPN"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return fmt.Errorf("NETINV_FLOW_TOPN=%q: want a positive integer", v)
			}
			agg.TopN = n
		}
		rt.Log.Info("flow aggregation", "top_n", agg.TopN)

		c := &flow.Collector{
			Addr: addr,
			Agg:  agg,
			// Flow writes its own series rather than going through the
			// ingester, so it has to mirror too — otherwise a backup holds
			// everything except the traffic composition.
			Write: flow.NewMirroredVMWriter(vmURL,
				vmwrite.ParseMirrors(os.Getenv("NETINV_VM_MIRROR_URL")), rt.Log),
			Log: rt.Log,
		}
		// Empty means accept from anywhere, which is right on a management
		// network and wrong on an untrusted one. Listing sources is the
		// difference between a collector and an open UDP sink that anyone can
		// write series into.
		if allow := os.Getenv("NETINV_FLOW_ALLOW"); allow != "" {
			prefixes, err := flow.ParseAllow(allow)
			if err != nil {
				return err
			}
			c.Allow = prefixes
			rt.Log.Info("flow source allow-list active", "prefixes", len(prefixes))
		} else {
			rt.Log.Warn("no NETINV_FLOW_ALLOW set — accepting flow from any source")
		}
		if chURL := os.Getenv("NETINV_FLOW_CH_URL"); chURL != "" {
			w, err := startRawStore(ctx, rt.Log, chURL)
			if err != nil {
				return err
			}
			c.Raw = w
		} else {
			rt.Log.Info("NETINV_FLOW_CH_URL not set — aggregates only, no raw flow storage")
		}
		rt.Health.SetReady(true)
		return c.Run(ctx)
	})
}

// startRawStore starts per-flow storage (ADR-026). ClickHouse may come up
// after this service, so the schema is applied in the background with
// retries; the writer queues meanwhile, within its bound, and its inserts
// simply fail and retry until the tables exist.
func startRawStore(ctx context.Context, log *slog.Logger, chURL string) (*flowstore.Writer, error) {
	ret := flowstore.DefaultRetention
	for env, dst := range map[string]*int{
		"NETINV_FLOW_RAW_RETENTION":    &ret.RawDays,
		"NETINV_FLOW_ROLLUP_RETENTION": &ret.RollupDays,
	} {
		if v := os.Getenv(env); v != "" {
			n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(v), "d"))
			if err != nil || n < 1 {
				return nil, fmt.Errorf("%s=%q: want a number of days, e.g. 30 or 30d", env, v)
			}
			*dst = n
		}
	}
	client := flowstore.NewClient(chURL, os.Getenv("NETINV_FLOW_CH_USER"), os.Getenv("NETINV_FLOW_CH_PASSWORD"))
	w := &flowstore.Writer{C: client, Log: log}

	go func() {
		for {
			err := flowstore.EnsureSchema(ctx, client, ret)
			if err == nil {
				log.Info("raw flow storage ready", "raw_days", ret.RawDays, "rollup_days", ret.RollupDays)
				return
			}
			log.Warn("raw flow storage not ready; retrying", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
		}
	}()
	go w.Run(ctx)
	go w.ReportStatus(ctx)

	// Country and AS-name enrichment, from files the operator downloads with
	// scripts/fetch-flow-geo.sh. Re-read when they change, so the monthly
	// refresh needs no restart.
	dir := os.Getenv("NETINV_FLOW_GEO_DIR")
	if dir == "" {
		dir = "/var/lib/netinv-flow/geo"
	}
	go geo.Watch(ctx, dir, 5*time.Minute, log, func(db *geo.DB) {
		w.SetGeo(db)
		asn, country := db.Loaded()
		log.Info("flow enrichment loaded", "dir", dir, "asn_ranges", asn, "country_ranges", country)
	})
	return w, nil
}
