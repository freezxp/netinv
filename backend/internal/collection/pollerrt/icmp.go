package pollerrt

import (
	"context"
	"math"
	"time"

	probing "github.com/prometheus-community/pro-bing"

	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

// probeICMP runs the availability probe (FR-COLL-05): N echoes → RTT
// min/avg/max, jitter (mean |ΔRTT| between consecutive replies), loss ratio.
// Unprivileged UDP-ICMP by default (needs net.ipv4.ping_group_range on
// Linux); set NETINV_ICMP_PRIVILEGED=1 for raw sockets + CAP_NET_RAW.
func probeICMP(ctx context.Context, job wire.PollJob, privileged bool) ([]wire.Sample, error) {
	// A probe that could not run is not a device that did not answer, and the
	// difference has to reach the metrics store. Returning only an error wrote
	// nothing at all, so `max_over_time(netinv_icmp_up[3m]) == 0` had no series
	// to evaluate and the availability rule went quiet rather than firing:
	// "the poller cannot send ICMP" — the unprivileged-ping permission case in
	// an LXC, for one — looked exactly like "nothing to report".
	//
	// It deliberately does not report the device as down. The poller does not
	// know whether it is: claiming a device is unreachable because we failed to
	// ask would page someone to look at healthy equipment.
	probeErr := func(err error) ([]wire.Sample, error) {
		return []wire.Sample{{
			DeviceID: job.DeviceID, Name: "netinv_icmp_probe_error",
			TSMillis: time.Now().UTC().UnixMilli(), Value: 1,
		}}, err
	}

	p, err := probing.NewPinger(job.MgmtIP)
	if err != nil {
		return probeErr(err)
	}
	p.SetPrivileged(privileged)
	p.Count = 5
	p.Interval = 200 * time.Millisecond
	p.Timeout = 4 * time.Second
	p.RecordRtts = true

	if err := p.RunWithContext(ctx); err != nil {
		return probeErr(err)
	}
	st := p.Statistics()
	now := time.Now().UTC().UnixMilli()
	up := 0.0
	if st.PacketsRecv > 0 {
		up = 1
	}
	loss := st.PacketLoss / 100 // pro-bing reports percent
	if math.IsNaN(loss) {
		loss = 1
	}
	s := func(name string, labels map[string]string, v float64) wire.Sample {
		return wire.Sample{DeviceID: job.DeviceID, Name: name, Labels: labels,
			TSMillis: now, Value: v}
	}
	samples := []wire.Sample{
		s("netinv_icmp_up", nil, up),
		s("netinv_icmp_loss_ratio", nil, loss),
		// Written on every successful probe so the series exists continuously.
		// A gauge that only appears when broken cannot be alerted on with `==
		// 1` without also matching every device that has never been probed.
		s("netinv_icmp_probe_error", nil, 0),
	}
	if st.PacketsRecv > 0 {
		jitter := 0.0
		if len(st.Rtts) > 1 {
			var sum float64
			for i := 1; i < len(st.Rtts); i++ {
				sum += math.Abs(st.Rtts[i].Seconds() - st.Rtts[i-1].Seconds())
			}
			jitter = sum / float64(len(st.Rtts)-1)
		}
		samples = append(samples,
			s("netinv_icmp_rtt_seconds", map[string]string{"stat": "min"}, st.MinRtt.Seconds()),
			s("netinv_icmp_rtt_seconds", map[string]string{"stat": "avg"}, st.AvgRtt.Seconds()),
			s("netinv_icmp_rtt_seconds", map[string]string{"stat": "max"}, st.MaxRtt.Seconds()),
			s("netinv_icmp_jitter_seconds", nil, jitter),
		)
	}
	return samples, nil
}
