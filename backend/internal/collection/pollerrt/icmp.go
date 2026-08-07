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
	p, err := probing.NewPinger(job.MgmtIP)
	if err != nil {
		return nil, err
	}
	p.SetPrivileged(privileged)
	p.Count = 5
	p.Interval = 200 * time.Millisecond
	p.Timeout = 4 * time.Second
	p.RecordRtts = true

	if err := p.RunWithContext(ctx); err != nil {
		return nil, err
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
