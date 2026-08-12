// netinv-flow — flow export collector (ADR-020, doc 34).
//
// Receives NetFlow, aggregates to top-N per interface, writes bounded
// cardinality series to VictoriaMetrics. It stores no per-flow record.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/freezxp/netinv/backend/internal/flow"
	"github.com/freezxp/netinv/backend/internal/platform/service"
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
			// Both conventional ports: NetFlow on 2055, IPFIX on 4739. An
			// exporter that cannot be told which to use will use its own
			// convention, and version is read from the datagram either way.
			addr = ":2055,:4739"
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
			Addr:  addr,
			Agg:   agg,
			Write: flow.NewVMWriter(vmURL),
			Log:   rt.Log,
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
		rt.Health.SetReady(true)
		return c.Run(ctx)
	})
}
