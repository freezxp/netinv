package postgres

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
	pgxp "github.com/freezxp/netinv/backend/internal/platform/pgx"
)

// MatchRule assigns one customer to every interface whose description text
// matches a pattern.
//
// It exists beside the CSV import because the two answer different questions.
// The import is for a list that already exists somewhere else, row by row. This
// is for the far commoner case where the operator already encoded the customer
// in the port description years ago — "ACME-WAN-01", "cust:globex" — and the
// answer is one rule, not a spreadsheet of two hundred rows.
type MatchRule struct {
	// Field names the column to match: alias, descr, name, or any, which
	// matches if any of the three does. Alias is the default because it is
	// the field operators curate; descr is usually the vendor's own text.
	Field string
	// Op is eq (whole value, case-insensitive) or like (contains).
	Op string
	// Value is the text to look for. Under like, % is the operator's own
	// wildcard and _ is matched literally — see likePattern.
	Value string
	// Customer is the name to write; ClearToken empties it.
	Customer string
	Tags     []string
	SetTags  bool
	// Overwrite allows replacing a customer already on the interface. Off by
	// default: re-tagging a port that another customer is already billed for
	// is the expensive mistake here, and a rule broad enough to do it looks
	// exactly like a rule that is working.
	Overwrite bool
	// Apply writes. The zero value previews, deliberately: a caller that
	// forgets this field gets the harmless behaviour, and this writes the
	// field reports bill from.
	Apply bool
}

// MatchPreviewRow is one interface a rule selected, as it stands now.
type MatchPreviewRow struct {
	Device   string `json:"device"`
	IfIndex  int    `json:"if_index"`
	Name     string `json:"name"`
	Alias    string `json:"alias"`
	Descr    string `json:"descr"`
	Customer string `json:"customer"`
}

// MatchResult reports what a rule selected and what it changed.
type MatchResult struct {
	Matched int  `json:"matched"`
	Updated int  `json:"updated"`
	DryRun  bool `json:"dry_run"`
	// Skipped names interfaces already assigned to a different customer,
	// which Overwrite would have taken. They are named rather than counted
	// because deciding whether the rule is too broad needs to see them.
	Skipped []string `json:"skipped"`
	// Sample is the first rows the rule selected, for a preview. Capped: the
	// point is to let someone recognise a wrong rule, and nobody reads the
	// four hundredth row to do that.
	Sample []MatchPreviewRow `json:"sample"`
}

const matchSampleCap = 25

// matchableCols whitelists the searchable columns. A whitelist rather than
// interpolation: this goes into the query text, and taking a column name from
// a request body is how a filter box becomes an injection.
var matchableCols = map[string]string{
	"alias": "coalesce(i.alias,'')",
	"descr": "coalesce(i.descr,'')",
	"name":  "coalesce(i.name,'')",
}

// likePattern turns the operator's text into a LIKE pattern.
//
// The two wildcards are treated differently on purpose, because operators mean
// them differently. **% is theirs**: someone typing "ACME-%-WAN" wants a
// pattern, and refusing it forces them into a substring that also catches
// things they did not ask for. **_ is not**: nearly every port alias contains
// an underscore and essentially nobody means "any character" by it, so a rule
// for "ACME_WAN" that also claimed "ACMExWAN" would be handing a different
// customer's port over on a typo nobody typed.
//
// Text with no % is wrapped as a substring, which is what "contains" should do
// and what the great majority of rules want. Text with a % is used verbatim
// and therefore anchored at both ends, so the operator gets the whole pattern
// language: "ACME%" is a prefix, "%ACME%" is contains, "A%B%C" is a shape.
func likePattern(s string) string {
	esc := strings.NewReplacer(`\`, `\\`, `_`, `\_`).Replace(s)
	if strings.Contains(esc, "%") {
		return esc
	}
	return "%" + esc + "%"
}

// onlyWildcards reports text that would select the entire fleet. A bare "%" is
// never a rule anyone meant to write, and it is one keystroke away from a real
// one — the preview would show it, but a rule that assigns every untagged port
// in the estate should have to be spelled out rather than typed by accident.
func onlyWildcards(s string) bool {
	return strings.Trim(s, "%_ \t") == ""
}

// AssignByMatch applies one rule to every matching interface.
//
// Select-then-update rather than a single UPDATE ... WHERE, because the caller
// needs to see what would change before it changes, and the preview and the
// write must select by identical criteria or the preview is a lie.
func (r *DeviceRepo) AssignByMatch(ctx context.Context, rule MatchRule) (MatchResult, error) {
	res := MatchResult{DryRun: !rule.Apply, Skipped: []string{}, Sample: []MatchPreviewRow{}}

	field := strings.ToLower(strings.TrimSpace(rule.Field))
	if field == "" {
		field = "alias"
	}
	op := strings.ToLower(strings.TrimSpace(rule.Op))
	if op == "" {
		op = "like"
	}
	value := strings.TrimSpace(rule.Value)
	if value == "" {
		return res, errx.New(errx.KindInvalid, "value is required")
	}
	if rule.Customer == "" && !rule.SetTags {
		return res, errx.New(errx.KindInvalid, "nothing to assign: give a customer or tags")
	}

	var cols []string
	if field == "any" {
		cols = []string{matchableCols["alias"], matchableCols["descr"], matchableCols["name"]}
	} else {
		c, ok := matchableCols[field]
		if !ok {
			return res, errx.New(errx.KindInvalid,
				"unknown field %q: use alias, descr, name or any", rule.Field)
		}
		cols = []string{c}
	}

	var preds []string
	var arg any
	switch op {
	case "eq":
		arg = value
		for _, c := range cols {
			preds = append(preds, "lower("+c+") = lower($1)")
		}
	case "like", "contains":
		if onlyWildcards(value) {
			return res, errx.New(errx.KindInvalid,
				"a value of only wildcards would match every interface in the fleet")
		}
		arg = likePattern(value)
		for _, c := range cols {
			preds = append(preds, c+` ILIKE $1 ESCAPE '\'`)
		}
	default:
		return res, errx.New(errx.KindInvalid, "unknown op %q: use eq or like", rule.Op)
	}

	// Retired devices and removed interfaces are excluded for the same reason
	// the search excludes them: they are not ports anyone can sell.
	q := `
		SELECT i.id, d.name, i.if_index, coalesce(i.name,''),
		       coalesce(i.alias,''), coalesce(i.descr,''), coalesce(i.customer,'')
		FROM inventory.interfaces i
		JOIN inventory.devices d ON d.id = i.device_id
		WHERE i.state != 'removed' AND d.status != 'retired'
		  AND (` + strings.Join(preds, " OR ") + `)
		ORDER BY d.name, i.if_index`

	rows, err := r.Pool.Query(ctx, q, arg)
	if err != nil {
		return res, errx.Wrap(errx.KindTransient, err, "match interfaces")
	}
	defer rows.Close()

	target := strings.TrimSpace(rule.Customer)
	var ids []string
	for rows.Next() {
		var id string
		var p MatchPreviewRow
		if err := rows.Scan(&id, &p.Device, &p.IfIndex, &p.Name,
			&p.Alias, &p.Descr, &p.Customer); err != nil {
			return res, err
		}
		res.Matched++
		if len(res.Sample) < matchSampleCap {
			res.Sample = append(res.Sample, p)
		}
		// Held by someone else, and the caller did not ask to take it.
		if !rule.Overwrite && target != "" && target != ClearToken &&
			p.Customer != "" && !strings.EqualFold(p.Customer, target) {
			res.Skipped = append(res.Skipped,
				p.Device+"/"+p.Name+" (held by "+p.Customer+")")
			continue
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return res, errx.Wrap(errx.KindTransient, err, "match interfaces")
	}
	if !rule.Apply || len(ids) == 0 {
		return res, nil
	}

	sets, args := []string{"updated_at = now()"}, []any{ids}
	if rule.Customer != "" {
		args = append(args, nullIfClear(rule.Customer))
		sets = append(sets, "customer = $"+itoa(len(args)))
	}
	if rule.SetTags {
		args = append(args, rule.Tags)
		sets = append(sets, "tags = $"+itoa(len(args)))
	}
	// One statement in one transaction: a half-applied rule leaves an operator
	// unable to tell which ports took it, and the same rule re-run is only
	// safe because it is idempotent, which a half-applied one is not.
	err = pgxp.InTx(ctx, r.Pool, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE inventory.interfaces SET `+
			strings.Join(sets, ", ")+` WHERE id = ANY($1)`, args...)
		if e != nil {
			return errx.Wrap(errx.KindTransient, e, "assign by match")
		}
		res.Updated = int(tag.RowsAffected())
		return nil
	})
	return res, err
}
