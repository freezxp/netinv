package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

type stubValidator struct {
	err  error
	seen []string
}

func (s *stubValidator) Query(_ context.Context, expr string) ([]Series, error) {
	s.seen = append(s.seen, expr)
	return nil, s.err
}

func TestValidateRequiresNameAndExpression(t *testing.T) {
	for name, in := range map[string]RuleInput{
		"no name":                {Expr: "up == 0"},
		"blank name":             {Name: "   ", Expr: "up == 0"},
		"threshold without expr": {Name: "x"},
	} {
		in := in
		if err := (&in).Validate(context.Background(), nil, true); err == nil {
			t.Errorf("%s: expected rejection", name)
		} else if errx.KindOf(err) != errx.KindInvalid {
			t.Errorf("%s: kind = %v, want invalid", name, errx.KindOf(err))
		}
	}
}

func TestValidateRejectsUnknownEnums(t *testing.T) {
	in := RuleInput{Name: "x", Expr: "up == 0", Severity: "urgent"}
	if err := in.Validate(context.Background(), nil, true); err == nil {
		t.Error("accepted an unknown severity")
	}
	in = RuleInput{Name: "x", Expr: "up == 0", Kind: "magic"}
	if err := in.Validate(context.Background(), nil, true); err == nil {
		t.Error("accepted an unknown kind")
	}
}

func TestValidateDefaultsOnCreate(t *testing.T) {
	in := RuleInput{Name: "  CPU hot  ", Expr: "  up == 0  "}
	if err := in.Validate(context.Background(), nil, true); err != nil {
		t.Fatal(err)
	}
	if in.Name != "CPU hot" || in.Expr != "up == 0" {
		t.Errorf("not trimmed: %q / %q", in.Name, in.Expr)
	}
	if in.Kind != "threshold" || in.Severity != "warning" {
		t.Errorf("defaults not applied: kind=%q severity=%q", in.Kind, in.Severity)
	}
}

// The check that earns its keep: the database accepts an unparseable
// expression happily and the only symptom is an alert that never fires.
func TestValidateRejectsExpressionTheBackendCannotParse(t *testing.T) {
	v := &stubValidator{err: errors.New(`query: cannot parse "up ==": missing operand`)}
	in := RuleInput{Name: "broken", Expr: "up =="}
	err := in.Validate(context.Background(), v, true)
	if err == nil {
		t.Fatal("accepted an expression the backend rejected")
	}
	if errx.KindOf(err) != errx.KindInvalid {
		t.Errorf("kind = %v, want invalid", errx.KindOf(err))
	}
	// The author needs the backend's reason, not a generic failure.
	if !strings.Contains(err.Error(), "missing operand") {
		t.Errorf("backend reason lost: %v", err)
	}
	if len(v.seen) != 1 || v.seen[0] != "up ==" {
		t.Errorf("validator saw %v", v.seen)
	}
}

// Update is a patch: omitted fields must not be treated as blanks to reject.
func TestValidateOnUpdateAllowsPartialInput(t *testing.T) {
	enabled := false
	in := RuleInput{Enabled: &enabled}
	if err := in.Validate(context.Background(), nil, false); err != nil {
		t.Fatalf("a disable-only patch was rejected: %v", err)
	}
	if in.Kind != "" || in.Severity != "" {
		t.Error("update filled in defaults, which would overwrite stored values")
	}
}

// An empty expression on update means "leave it alone", so it must not be sent
// to the backend for validation.
func TestValidateSkipsBackendWhenNoExpressionGiven(t *testing.T) {
	v := &stubValidator{err: errors.New("should not be called")}
	in := RuleInput{Name: "rename only"}
	if err := in.Validate(context.Background(), v, false); err != nil {
		t.Fatal(err)
	}
	if len(v.seen) != 0 {
		t.Errorf("validated %v, want nothing", v.seen)
	}
}

func TestBuiltinRulesCannotBeDeleted(t *testing.T) {
	err := DeleteGuard(&RuleSummary{Name: "Interface down", Builtin: true})
	if err == nil {
		t.Fatal("a built-in rule was deletable")
	}
	if errx.KindOf(err) != errx.KindConflict {
		t.Errorf("kind = %v, want conflict", errx.KindOf(err))
	}
	// The message has to point somewhere useful.
	if !strings.Contains(err.Error(), "disable") {
		t.Errorf("message offers no alternative: %v", err)
	}
	if err := DeleteGuard(&RuleSummary{Name: "mine", Builtin: false}); err != nil {
		t.Errorf("an operator's own rule was blocked: %v", err)
	}
}

// Regression: an earlier queryError cut everything before the last ": " to
// strip transport prefixes. VictoriaMetrics ends parse errors with
// `unparsed data: ""`, so the operator was shown two quote marks and nothing
// else.
func TestBackendErrorSurvivesIntact(t *testing.T) {
	vmMsg := `error when executing query="x >": singleExpr: unexpected token ""; ` +
		`want "(", "{"; unparsed data: ""`
	v := &stubValidator{err: errors.New(vmMsg)}
	in := RuleInput{Name: "broken", Expr: "x >"}
	err := in.Validate(context.Background(), v, true)
	if err == nil {
		t.Fatal("expected rejection")
	}
	if !strings.Contains(err.Error(), "unexpected token") {
		t.Errorf("the actionable part was trimmed away: %v", err)
	}
}
