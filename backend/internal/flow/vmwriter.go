package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// VMWriter publishes aggregates to VictoriaMetrics via its import endpoint,
// the same path the metric ingester uses.
type VMWriter struct {
	BaseURL string
	HTTP    *http.Client
}

func NewVMWriter(baseURL string) *VMWriter {
	return &VMWriter{BaseURL: baseURL, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

type importLine struct {
	Metric     map[string]string `json:"metric"`
	Values     []float64         `json:"values"`
	Timestamps []int64           `json:"timestamps"`
}

// Metric names. Bytes and packets are counters over the aggregation interval
// rather than cumulative totals: a top-N membership changes between intervals,
// so a cumulative series would reset whenever a talker dropped out of the
// table and rate() would read that as a counter wrap.
const (
	MetricBytes   = "netinv_flow_bytes"
	MetricPackets = "netinv_flow_packets"
)

func (w *VMWriter) WriteFlow(ctx context.Context, at time.Time, buckets []Bucket) error {
	if len(buckets) == 0 {
		return nil
	}
	ts := at.UnixMilli()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, b := range buckets {
		labels := map[string]string{
			"exporter":  b.Key.ExporterIP,
			"if_index":  itoa(uint64(b.Key.IfIndex)),
			"dimension": string(b.Key.Dimension),
			"value":     b.Key.Value,
		}
		// Sampled is a label rather than a separate metric so a chart can show
		// both without the MetricsQL `or` trap that has bitten this codebase
		// three times — two metrics differing only by name collapse into one.
		if b.Sampled {
			labels["sampled"] = "true"
		}
		for name, v := range map[string]float64{
			MetricBytes:   float64(b.Bytes),
			MetricPackets: float64(b.Packets),
		} {
			m := make(map[string]string, len(labels)+1)
			for k, val := range labels {
				m[k] = val
			}
			m["__name__"] = name
			if err := enc.Encode(importLine{
				Metric: m, Values: []float64{v}, Timestamps: []int64{ts},
			}); err != nil {
				return fmt.Errorf("flow: encode: %w", err)
			}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		w.BaseURL+"/api/v1/import", &buf)
	if err != nil {
		return fmt.Errorf("flow: request: %w", err)
	}
	resp, err := w.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("flow: post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("flow: metrics store returned %d", resp.StatusCode)
	}
	return nil
}
