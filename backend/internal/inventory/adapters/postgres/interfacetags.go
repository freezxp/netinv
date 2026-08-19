package postgres

import (
	"context"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
	pgxp "github.com/freezxp/netinv/backend/internal/platform/pgx"
)

// TagAssignment names one interface and what to put on it.
//
// The interface is named the way an operator writes it down — a device by name
// or management address, a port by name or ifIndex — because the list being
// imported was almost certainly typed or exported somewhere else, and
// demanding internal ids would mean looking up every row by hand first, which
// is the work the import exists to avoid.
type TagAssignment struct {
	Device    string   // device name or management IP
	Interface string   // interface name or ifIndex
	Customer  string   // "" leaves it unchanged; "-" clears it
	Tags      []string // nil leaves them unchanged
	SetTags   bool     // true when the row specified tags at all
}

// TagResult reports what an import did, row by row for the failures.
type TagResult struct {
	Matched   int      `json:"matched"`
	Updated   int      `json:"updated"`
	Unmatched []string `json:"unmatched"`
	Ambiguous []string `json:"ambiguous"`
}

// ClearToken empties a field. An import has to be able to un-assign a circuit
// — a customer leaves — and a blank cell cannot mean that, because a blank
// cell in a spreadsheet overwhelmingly means "I did not fill this in".
const ClearToken = "-"

// ApplyTags assigns customers and tags to interfaces in one transaction.
//
// All or nothing: a partially applied import leaves an operator unable to tell
// which half took, and re-running it is only safe if it is idempotent, which a
// half-applied one is not.
func (r *DeviceRepo) ApplyTags(ctx context.Context, in []TagAssignment) (TagResult, error) {
	res := TagResult{Unmatched: []string{}, Ambiguous: []string{}}
	err := pgxp.InTx(ctx, r.Pool, func(tx pgx.Tx) error {
		for _, a := range in {
			dev, iface := strings.TrimSpace(a.Device), strings.TrimSpace(a.Interface)
			if dev == "" || iface == "" {
				res.Unmatched = append(res.Unmatched, dev+"/"+iface+" (device and interface are both required)")
				continue
			}
			ids, err := findInterfaceIDs(ctx, tx, dev, iface)
			if err != nil {
				return err
			}
			switch len(ids) {
			case 0:
				res.Unmatched = append(res.Unmatched, dev+"/"+iface)
				continue
			case 1:
			default:
				// Two devices sharing a name, or an ifIndex that matches a
				// port name on the same device. Guessing here writes a
				// customer onto the wrong circuit, which surfaces as a wrong
				// invoice rather than an error.
				res.Ambiguous = append(res.Ambiguous, dev+"/"+iface)
				continue
			}
			res.Matched++

			sets, args := []string{"updated_at = now()"}, []any{ids[0]}
			if a.Customer != "" {
				args = append(args, nullIfClear(a.Customer))
				sets = append(sets, "customer = $"+itoa(len(args)))
			}
			if a.SetTags {
				args = append(args, a.Tags)
				sets = append(sets, "tags = $"+itoa(len(args)))
			}
			if len(sets) == 1 {
				continue // nothing to change on this row
			}
			tag, err := tx.Exec(ctx,
				`UPDATE inventory.interfaces SET `+strings.Join(sets, ", ")+
					` WHERE id = $1`, args...)
			if err != nil {
				return errx.Wrap(errx.KindTransient, err, "apply interface tags")
			}
			res.Updated += int(tag.RowsAffected())
		}
		return nil
	})
	return res, err
}

// findInterfaceIDs resolves a device+interface pair written the way a human
// writes it. Both sides accept two spellings, and both are matched
// case-insensitively — a spreadsheet's "GE-0/0/1" and the device's "ge-0/0/1"
// are the same port, and rejecting the row for it would be pedantry.
func findInterfaceIDs(ctx context.Context, tx pgx.Tx, device, iface string) ([]string, error) {
	ifIndex := -1
	if n, err := strconv.Atoi(iface); err == nil {
		ifIndex = n
	}
	rows, err := tx.Query(ctx, `
		SELECT i.id
		FROM inventory.interfaces i
		JOIN inventory.devices d ON d.id = i.device_id
		WHERE d.status != 'retired' AND i.state != 'removed'
		  AND (lower(d.name) = lower($1) OR host(d.mgmt_ip) = $1)
		  AND (lower(i.name) = lower($2) OR i.if_index = $3)`,
		device, iface, ifIndex)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "resolve interface")
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func nullIfClear(v string) *string {
	if v == ClearToken {
		return nil
	}
	return &v
}

// Customers lists the assigned customer names with how many interfaces each
// holds, for the filter dropdown. Reading them from the data rather than from
// a customers table is deliberate: there is no customer entity in NetInv, and
// inventing one would demand an onboarding step before anyone could tag a
// single port.
func (r *DeviceRepo) Customers(ctx context.Context) ([]CustomerCount, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT i.customer, count(*)
		FROM inventory.interfaces i
		JOIN inventory.devices d ON d.id = i.device_id
		WHERE i.customer IS NOT NULL AND i.customer <> ''
		  AND i.state != 'removed' AND d.status != 'retired'
		GROUP BY i.customer
		ORDER BY i.customer`)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "customers")
	}
	defer rows.Close()
	out := []CustomerCount{}
	for rows.Next() {
		var c CustomerCount
		if err := rows.Scan(&c.Customer, &c.Interfaces); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type CustomerCount struct {
	Customer   string `json:"customer"`
	Interfaces int    `json:"interfaces"`
}
