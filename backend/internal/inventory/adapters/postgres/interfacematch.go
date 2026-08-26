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
// MatchCondition is one test against one text column.
type MatchCondition struct {
	// Field names the column to match: alias, descr, name, or any, which
	// matches if any of the three does. Alias is the default because it is
	// the field operators curate; descr is usually the vendor's own text.
	Field string `json:"field"`
	// Op is eq (whole value, case-insensitive) or like (contains).
	Op string `json:"op"`
	// Value is the text to look for. Under like, % is the operator's own
	// wildcard and _ is matched literally — see likePattern.
	Value string `json:"value"`
}

type MatchRule struct {
	// Conditions are ANDed. Two conditions is the common shape — "the alias
	// says ACME *and* it says 8585" — because one substring is usually either
	// too broad or too specific on its own, and narrowing by re-typing a
	// longer string only works when the two fragments are adjacent.
	//
	// A single condition may instead be given in Field/Op/Value, which is
	// what the earlier single-filter form sent.
	Conditions []MatchCondition
	// Field, Op and Value are the single-condition form. Ignored when
	// Conditions is set.
	Field string
	Op    string
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

	conds := rule.Conditions
	if len(conds) == 0 {
		conds = []MatchCondition{{Field: rule.Field, Op: rule.Op, Value: rule.Value}}
	}
	if rule.Customer == "" && !rule.SetTags {
		return res, errx.New(errx.KindInvalid, "nothing to assign: give a customer or tags")
	}
	if len(conds) > maxConditions {
		return res, errx.New(errx.KindInvalid,
			"a rule takes at most %d conditions", maxConditions)
	}

	// Each condition contributes one ANDed clause and one argument. Built as
	// numbered placeholders rather than interpolated text: the values are
	// operator input, and the column names come from a whitelist.
	var clauses []string
	var args []any
	for _, c := range conds {
		clause, arg, err := condClause(c, len(args)+1)
		if err != nil {
			return res, err
		}
		clauses = append(clauses, clause)
		args = append(args, arg)
	}

	// Retired devices and removed interfaces are excluded for the same reason
	// the search excludes them: they are not ports anyone can sell.
	q := `
		SELECT i.id, d.name, i.if_index, coalesce(i.name,''),
		       coalesce(i.alias,''), coalesce(i.descr,''), coalesce(i.customer,'')
		FROM inventory.interfaces i
		JOIN inventory.devices d ON d.id = i.device_id
		WHERE i.state != 'removed' AND d.status != 'retired'
		  AND ` + strings.Join(clauses, "\n\t\t  AND ") + `
		ORDER BY d.name, i.if_index`

	rows, err := r.Pool.Query(ctx, q, args...)
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

// maxConditions bounds a rule. Each one is another ANDed clause over the same
// table scan; past a handful the rule has stopped being something an operator
// can read back and check, which is the property the preview depends on.
const maxConditions = 8

// condClause renders one condition as a SQL predicate plus its argument.
func condClause(c MatchCondition, argN int) (string, any, error) {
	field := strings.ToLower(strings.TrimSpace(c.Field))
	if field == "" {
		field = "alias"
	}
	op := strings.ToLower(strings.TrimSpace(c.Op))
	if op == "" {
		op = "like"
	}
	value := strings.TrimSpace(c.Value)
	if value == "" {
		return "", nil, errx.New(errx.KindInvalid, "every condition needs a value")
	}

	var cols []string
	if field == "any" {
		cols = []string{matchableCols["alias"], matchableCols["descr"], matchableCols["name"]}
	} else {
		col, ok := matchableCols[field]
		if !ok {
			return "", nil, errx.New(errx.KindInvalid,
				"unknown field %q: use alias, descr, name or any", c.Field)
		}
		cols = []string{col}
	}

	ph := "$" + itoa(argN)
	var preds []string
	var arg any
	switch op {
	case "eq":
		arg = value
		for _, col := range cols {
			preds = append(preds, "lower("+col+") = lower("+ph+")")
		}
	case "like", "contains":
		if onlyWildcards(value) {
			return "", nil, errx.New(errx.KindInvalid,
				"a value of only wildcards would match every interface in the fleet")
		}
		arg = likePattern(value)
		for _, col := range cols {
			preds = append(preds, col+` ILIKE `+ph+` ESCAPE '\'`)
		}
	default:
		return "", nil, errx.New(errx.KindInvalid, "unknown op %q: use eq or like", c.Op)
	}
	return "(" + strings.Join(preds, " OR ") + ")", arg, nil
}
