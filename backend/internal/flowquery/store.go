// Package flowquery serves the flow aggregates to API clients (doc 09 §7.1).
//
// The collector (package flow) writes netinv_flow_bytes/packets keyed by the
// address a datagram came from; this package is the read side that turns those
// series into answers about devices. Two things it does that a client querying
// the metrics proxy directly would have to get right on its own, and usually
// does not: it maps exporter addresses to devices (management address plus
// claimed flow_exporters, doc 34 §3.1), and it sums per-interval samples rather
// than taking rate() of them — the series are not cumulative, and rate() of a
// per-interval value silently under-reports it.
package flowquery

import (
	"context"
	"net/netip"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

// Attribution is one address a device can export flow from.
type Attribution struct {
	Addr       string
	DeviceID   string
	DeviceName string
	SiteID     string
	// Claimed is true for an address from flow_exporters rather than the
	// management address.
	Claimed bool
}

// AddressBook resolves between exporter addresses and devices.
type AddressBook interface {
	Addresses(ctx context.Context) ([]Attribution, error)
}

type Store struct{ Pool *pgxpool.Pool }

// Addresses lists every address a live device is reachable as, management
// addresses first. Retired devices are excluded so an address reassigned to a
// new device stops resolving to the one that used to hold it — the same rule
// the trap listener applies (doc 36).
func (s *Store) Addresses(ctx context.Context) ([]Attribution, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT host(mgmt_ip), id, name, site_id, false FROM inventory.devices
		WHERE status <> 'retired'
		UNION ALL
		SELECT e, d.id, d.name, d.site_id, true
		FROM inventory.devices d,
		     jsonb_array_elements_text(
		       CASE WHEN jsonb_typeof(d.attrs->'flow_exporters') = 'array'
		            THEN d.attrs->'flow_exporters' ELSE '[]'::jsonb END) e
		WHERE d.status <> 'retired'
		ORDER BY 5, 2`)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "load flow exporter addresses")
	}
	defer rows.Close()
	var out []Attribution
	for rows.Next() {
		var a Attribution
		if err := rows.Scan(&a.Addr, &a.DeviceID, &a.DeviceName, &a.SiteID, &a.Claimed); err != nil {
			return nil, errx.Wrap(errx.KindTransient, err, "scan flow exporter address")
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// index maps a normalised address to the device it belongs to. The first
// attribution wins, and Addresses sorts management addresses first, so a
// claimed exporter address can never take a device's own address from it.
type index map[string]Attribution

func newIndex(list []Attribution) index {
	x := index{}
	for _, a := range list {
		k := normAddr(a.Addr)
		if _, seen := x[k]; !seen {
			x[k] = a
		}
	}
	return x
}

// normAddr makes "::ffff:192.0.2.1" and "192.0.2.1" the same key: the
// collector unmaps v4-in-v6 sources, an operator typing a claim may not.
func normAddr(s string) string {
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap().String()
	}
	return s
}
