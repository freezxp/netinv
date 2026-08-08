package pollerrt

import (
	"context"
	"net/netip"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/freezxp/netinv/backend/internal/platform/amqpx"
	"github.com/freezxp/netinv/backend/internal/platform/wire"
)

// Discovery sweep limits (doc 11 §7): bounded concurrency keeps the scan
// IDS-friendly and gentle on the network, and the host cap stops a fat CIDR
// from turning into an hours-long job.
const (
	discoveryConcurrency = 48
	discoveryMaxHosts    = 4096
	discoveryProbeOIDs   = 3
)

// executeDiscovery sweeps a CIDR, probing each address with the candidate
// credentials in order and reporting the ones that answer SNMP.
//
// The probe is SNMP-only by design: a device that doesn't answer SNMP can't be
// managed by NetInv anyway, and many devices drop ICMP while answering SNMP
// fine — so gating on a ping would hide manageable devices.
func (r *Runtime) executeDiscovery(ctx context.Context, job wire.DiscoveryJob) error {
	res := wire.DiscoveryResult{
		JobID: job.JobID, RuleID: job.RuleID, PollerID: r.PollerID,
	}
	hosts, err := expandCIDR(job.CIDR)
	if err != nil {
		res.Error = err.Error()
		return r.Client.PublishJSON(ctx, "", amqpx.DiscoveryResultsQueue, res)
	}
	res.Scanned = len(hosts)

	timeout := time.Duration(job.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}
	port := job.Port
	if port == 0 {
		port = 161
	}

	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		sem   = make(chan struct{}, discoveryConcurrency)
		found []wire.DiscoveredHost
	)
	for _, ip := range hosts {
		select {
		case <-ctx.Done():
			wg.Wait()
			res.Found = found
			res.Error = "cancelled"
			return r.Client.PublishJSON(context.WithoutCancel(ctx), "",
				amqpx.DiscoveryResultsQueue, res)
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			defer func() { <-sem }()
			if host, ok := probeHost(ctx, ip, port, timeout, job.Creds); ok {
				mu.Lock()
				found = append(found, host)
				mu.Unlock()
			}
		}(ip)
	}
	wg.Wait()
	res.Found = found
	r.Log.Info("discovery sweep complete", "cidr", job.CIDR,
		"scanned", res.Scanned, "found", len(found))
	return r.Client.PublishJSON(ctx, "", amqpx.DiscoveryResultsQueue, res)
}

// probeHost tries each candidate credential until one answers the system group.
func probeHost(ctx context.Context, ip string, port int, timeout time.Duration,
	creds []wire.NamedCred) (wire.DiscoveredHost, bool) {
	for _, c := range creds {
		job := wire.PollJob{MgmtIP: ip, Port: port, Cred: c.Cred,
			TimeoutMS: int(timeout.Milliseconds()), Retries: 0}
		sess, err := NewSNMPSession(job)
		if err != nil {
			continue
		}
		vars, err := sess.Get(ctx, []string{
			".1.3.6.1.2.1.1.5.0", ".1.3.6.1.2.1.1.1.0", ".1.3.6.1.2.1.1.2.0",
		})
		sess.Close()
		if err != nil || len(vars) == 0 {
			continue
		}
		host := wire.DiscoveredHost{IP: ip, CredentialID: c.CredentialID}
		for _, v := range vars {
			switch v.OID {
			case ".1.3.6.1.2.1.1.5.0":
				host.SysName = snmpString(v.Value)
			case ".1.3.6.1.2.1.1.1.0":
				host.SysDescr = snmpString(v.Value)
			case ".1.3.6.1.2.1.1.2.0":
				host.SysObjectID = snmpString(v.Value)
			}
		}
		// An agent that answers but returns nothing useful isn't a find.
		if host.SysName == "" && host.SysDescr == "" {
			continue
		}
		return host, true
	}
	return wire.DiscoveredHost{}, false
}

func snmpString(v any) string {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case string:
		return x
	case gosnmp.SnmpPDU:
		return ""
	}
	if s, ok := v.(interface{ String() string }); ok {
		return s.String()
	}
	return ""
}

// expandCIDR lists the usable host addresses of an IPv4 prefix.
func expandCIDR(cidr string) ([]string, error) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return nil, err
	}
	p = p.Masked()
	if !p.Addr().Is4() {
		return nil, errTooLarge("only IPv4 prefixes are supported")
	}
	total := 1 << (32 - p.Bits())
	if total > discoveryMaxHosts+2 {
		return nil, errTooLarge("prefix too large: max /20 (4096 hosts) per sweep")
	}
	var out []string
	addr := p.Addr()
	for i := 0; i < total; i++ {
		if i > 0 || p.Bits() >= 31 { // skip network address on real subnets
			if !(p.Bits() < 31 && i == total-1) { // and the broadcast address
				out = append(out, addr.String())
			}
		}
		addr = addr.Next()
	}
	return out, nil
}

type errTooLarge string

func (e errTooLarge) Error() string { return string(e) }
