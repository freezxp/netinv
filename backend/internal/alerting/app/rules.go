package app

import (
	"context"
	"strings"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

// RuleInput is an operator-authored alert rule (FR-ALR-01). Pointer fields are
// "leave alone" on update, so a caller can toggle one thing without restating
// the rest.
type RuleInput struct {
	Name        string         `json:"name"`
	Kind        string         `json:"kind"`
	Severity    string         `json:"severity"`
	Expr        string         `json:"expr"`
	Condition   map[string]any `json:"condition"`
	Annotations map[string]any `json:"annotations"`
	Enabled     *bool          `json:"enabled"`
}

var (
	ruleKinds      = map[string]bool{"threshold": true, "state": true, "inventory": true}
	ruleSeverities = map[string]bool{"critical": true, "warning": true, "info": true}
)

// ExprValidator checks that an expression is one the metrics backend accepts.
type ExprValidator interface {
	Query(ctx context.Context, expr string) ([]Series, error)
}

// Validate applies the rules a human would otherwise discover by watching an
// alert never fire.
//
// The expression check is the valuable one: an unparseable expr is accepted by
// the database quite happily, and the only symptom is silence — the evaluator
// logs a warning each cycle and the rule never matches anything. Better to
// refuse it while someone is looking at the form.
func (in *RuleInput) Validate(ctx context.Context, v ExprValidator, creating bool) error {
	in.Name = strings.TrimSpace(in.Name)
	in.Expr = strings.TrimSpace(in.Expr)

	if creating || in.Name != "" {
		if in.Name == "" {
			return errx.New(errx.KindInvalid, "name is required")
		}
		if len(in.Name) > 120 {
			return errx.New(errx.KindInvalid, "name must be 120 characters or fewer")
		}
	}
	if in.Kind != "" && !ruleKinds[in.Kind] {
		return errx.New(errx.KindInvalid,
			"kind must be one of threshold, state, inventory")
	}
	if in.Severity != "" && !ruleSeverities[in.Severity] {
		return errx.New(errx.KindInvalid,
			"severity must be one of critical, warning, info")
	}
	if creating {
		if in.Kind == "" {
			in.Kind = "threshold"
		}
		if in.Severity == "" {
			in.Severity = "warning"
		}
		// Only threshold rules are evaluated today; the other kinds exist for
		// event sources that are not wired yet (doc 05), and a rule with no
		// expression would sit inert without saying why.
		if in.Kind == "threshold" && in.Expr == "" {
			return errx.New(errx.KindInvalid,
				"a threshold rule needs an expression to evaluate")
		}
	}
	if in.Expr != "" && v != nil {
		if _, err := v.Query(ctx, in.Expr); err != nil {
			return errx.New(errx.KindInvalid,
				"the metrics backend rejected this expression: %s", queryError(err))
		}
	}
	return nil
}

// queryError caps a backend error to something a form can show.
//
// Deliberately no clever trimming: an earlier version cut everything before the
// last ": " to drop transport prefixes, which mangled the useful messages —
// VictoriaMetrics ends its parse errors with `unparsed data: ""`, so trimming
// left exactly that and nothing else.
func queryError(err error) string {
	msg := strings.TrimSpace(err.Error())
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}

// RuleSummary is the read model for the rules list.
type RuleSummary struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Kind        string         `json:"kind"`
	Severity    string         `json:"severity"`
	Expr        string         `json:"expr"`
	Condition   map[string]any `json:"condition"`
	Annotations map[string]any `json:"annotations"`
	Enabled     bool           `json:"enabled"`
	Builtin     bool           `json:"is_builtin"`
	// Firing counts live alerts, so the UI can warn before disabling a rule
	// that is currently reporting something.
	Firing int `json:"firing"`
}

// DeleteGuard reports why a rule may not be removed, or empty when it may.
//
// Built-in rules are the shipped pack (FR-ALR-07): they can be edited, tuned
// and disabled, but not deleted, because nothing would restore them short of a
// re-migration and their ids are referenced by docs and runbooks.
func DeleteGuard(r *RuleSummary) error {
	if r.Builtin {
		return errx.New(errx.KindConflict,
			"%q is a built-in rule and cannot be deleted — disable it instead",
			r.Name)
	}
	return nil
}
