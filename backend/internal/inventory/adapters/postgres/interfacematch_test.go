package postgres

import (
	"testing"

	"github.com/freezxp/netinv/backend/internal/inventory/domain"
	"github.com/freezxp/netinv/backend/internal/platform/id"
)

// seedMatchFixture builds one device with ports whose aliases are the shapes
// that break a naive rule: a wildcard character, a name that contains another
// customer's name, and a port already sold to someone else.
func seedMatchFixture(t *testing.T) (*DeviceRepo, string, func() []InterfaceSearchRow) {
	t.Helper()
	repo, dr, ctx := newSiteRepo(t)
	site := addSite(t, repo, ctx, "match-site")
	addDevice(t, dr, ctx, site.ID, "10.95.0.1", domain.DevicePending)

	var deviceID string
	if err := dr.Pool.QueryRow(ctx,
		`SELECT id FROM inventory.devices WHERE site_id = $1`, site.ID).
		Scan(&deviceID); err != nil {
		t.Fatalf("find device: %v", err)
	}
	for _, i := range []struct {
		idx      int
		name     string
		alias    string
		descr    string
		customer *string
	}{
		{10, "ge-0/0/0", "ACME-WAN-01", "uplink", nil},
		{11, "ge-0/0/1", "ACME-WAN-02", "uplink", nil},
		{12, "ge-0/0/2", "ACME_WAN-03", "uplink", nil},
		{13, "ge-0/0/3", "ACMExWAN-04", "uplink", nil},
		{14, "ge-0/0/4", "GLOBEX-WAN-01", "acme in the descr", nil},
		{15, "ge-0/0/5", "ACME-WAN-05", "uplink", strp("Globex Ltd")},
	} {
		if _, err := dr.Pool.Exec(ctx, `
			INSERT INTO inventory.interfaces
			  (id, device_id, if_index, name, alias, descr, customer, state)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'present')`,
			id.New("if"), deviceID, i.idx, i.name, i.alias, i.descr, i.customer); err != nil {
			t.Fatalf("seed interface: %v", err)
		}
	}
	all := func() []InterfaceSearchRow {
		rows, _, err := dr.FindInterfaces(ctx, InterfaceFilter{}, 0, 0)
		if err != nil {
			t.Fatalf("list interfaces: %v", err)
		}
		return rows
	}
	return dr, site.ID, all
}

func strp(s string) *string { return &s }

// A dry run must select exactly what the write would, and change nothing.
// Otherwise the preview an operator approves is not the change they get.
func TestAssignByMatchDryRunPreviewsWithoutWriting(t *testing.T) {
	dr, _, all := seedMatchFixture(t)
	ctx := t.Context()

	dry, err := dr.AssignByMatch(ctx, MatchRule{
		Field: "alias", Op: "like", Value: "ACME-WAN", Customer: "Acme Ltd",
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !dry.DryRun {
		t.Fatal("result does not report itself as a dry run")
	}
	if dry.Updated != 0 {
		t.Fatalf("dry run updated %d rows, want 0", dry.Updated)
	}
	for _, r := range all() {
		if r.Customer == "Acme Ltd" {
			t.Fatalf("dry run wrote a customer onto %s", r.Name)
		}
	}

	wet, err := dr.AssignByMatch(ctx, MatchRule{
		Field: "alias", Op: "like", Value: "ACME-WAN", Customer: "Acme Ltd",
		Apply: true,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if wet.Matched != dry.Matched {
		t.Fatalf("apply matched %d but the preview said %d", wet.Matched, dry.Matched)
	}
}

// The operator's own underscore is a literal. An unescaped LIKE would let
// "ACME_WAN" also claim "ACMExWAN", which is a different customer's port.
func TestAssignByMatchTreatsUnderscoreAsLiteral(t *testing.T) {
	dr, _, all := seedMatchFixture(t)
	ctx := t.Context()

	res, err := dr.AssignByMatch(ctx, MatchRule{
		Field: "alias", Op: "like", Value: "ACME_WAN", Customer: "Acme Ltd",
		Apply: true,
	})
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if res.Matched != 1 {
		t.Fatalf("matched %d ports, want only the literal ACME_WAN one", res.Matched)
	}
	for _, r := range all() {
		if r.IfIndex == 13 && r.Customer != "" {
			t.Fatalf("ACMExWAN-04 was claimed by an unescaped underscore")
		}
	}
}

// A port already sold to someone else is left alone and named, unless the
// caller explicitly asks to take it.
func TestAssignByMatchWillNotStealAnotherCustomersPort(t *testing.T) {
	dr, _, all := seedMatchFixture(t)
	ctx := t.Context()

	res, err := dr.AssignByMatch(ctx, MatchRule{
		Field: "alias", Op: "like", Value: "ACME-WAN", Customer: "Acme Ltd",
		Apply: true,
	})
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("skipped = %v, want the one port held by Globex", res.Skipped)
	}
	for _, r := range all() {
		if r.IfIndex == 15 && r.Customer != "Globex Ltd" {
			t.Fatalf("held port was reassigned to %q", r.Customer)
		}
	}

	// With overwrite the same rule takes it, which is the point of the flag.
	if _, err := dr.AssignByMatch(ctx, MatchRule{
		Field: "alias", Op: "like", Value: "ACME-WAN", Customer: "Acme Ltd",
		Overwrite: true, Apply: true,
	}); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	for _, r := range all() {
		if r.IfIndex == 15 && r.Customer != "Acme Ltd" {
			t.Fatalf("overwrite did not take the port: %q", r.Customer)
		}
	}
}

// eq is the whole value, not a substring: it must not pick up the longer
// aliases that merely start with it.
func TestAssignByMatchEqMatchesTheWholeValue(t *testing.T) {
	dr, _, _ := seedMatchFixture(t)
	ctx := t.Context()

	res, err := dr.AssignByMatch(ctx, MatchRule{
		Field: "alias", Op: "eq", Value: "acme-wan-01", Customer: "Acme Ltd",
	})
	if err != nil {
		t.Fatalf("eq: %v", err)
	}
	if res.Matched != 1 {
		t.Fatalf("eq matched %d, want exactly the one whole-value hit", res.Matched)
	}
}

// any searches the other columns too, so a customer named only in the vendor's
// descr is still found.
func TestAssignByMatchAnySearchesEveryTextColumn(t *testing.T) {
	dr, _, _ := seedMatchFixture(t)
	ctx := t.Context()

	alias, err := dr.AssignByMatch(ctx, MatchRule{
		Field: "alias", Op: "like", Value: "acme", Customer: "Acme Ltd",
	})
	if err != nil {
		t.Fatalf("alias: %v", err)
	}
	any, err := dr.AssignByMatch(ctx, MatchRule{
		Field: "any", Op: "like", Value: "acme", Customer: "Acme Ltd",
	})
	if err != nil {
		t.Fatalf("any: %v", err)
	}
	if any.Matched <= alias.Matched {
		t.Fatalf("any matched %d, alias %d — any must also reach the descr",
			any.Matched, alias.Matched)
	}
}

// A rule with no value would match every port in the fleet.
func TestAssignByMatchRejectsRulesThatWouldMatchEverything(t *testing.T) {
	dr, _, _ := seedMatchFixture(t)
	ctx := t.Context()

	if _, err := dr.AssignByMatch(ctx, MatchRule{
		Field: "alias", Op: "like", Value: "  ", Customer: "Acme Ltd",
	}); err == nil {
		t.Fatal("an empty value was accepted")
	}
	if _, err := dr.AssignByMatch(ctx, MatchRule{
		Field: "alias", Op: "like", Value: "acme",
	}); err == nil {
		t.Fatal("a rule assigning nothing was accepted")
	}
	if _, err := dr.AssignByMatch(ctx, MatchRule{
		Field: "customer; DROP TABLE inventory.interfaces --", Op: "like",
		Value: "acme", Customer: "Acme Ltd",
	}); err == nil {
		t.Fatal("an unknown field was accepted into the query")
	}
}

// % is the operator's wildcard, so a shape they type is the shape they get.
// Anchored at both ends when they use one: "ACME-WAN-0%" must not reach the
// alias that merely contains that text somewhere in the middle.
func TestAssignByMatchHonoursPercentAsAWildcard(t *testing.T) {
	dr, _, _ := seedMatchFixture(t)
	ctx := t.Context()

	// A shape: dash-separated, ending in WAN-something.
	shape, err := dr.AssignByMatch(ctx, MatchRule{
		Field: "alias", Op: "like", Value: "ACME-%-0%", Customer: "Acme Ltd",
	})
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	// ACME-WAN-01, -02 and -05. Not ACME_WAN-03 (literal underscore), not
	// ACMExWAN-04, not the GLOBEX port.
	if shape.Matched != 3 {
		t.Fatalf("shape matched %d, want 3: %+v", shape.Matched, shape.Sample)
	}

	// A prefix is anchored: nothing here starts with WAN, though three
	// aliases contain it, so an unanchored pattern would wrongly match.
	prefix, err := dr.AssignByMatch(ctx, MatchRule{
		Field: "alias", Op: "like", Value: "WAN%", Customer: "Acme Ltd",
	})
	if err != nil {
		t.Fatalf("prefix: %v", err)
	}
	if prefix.Matched != 0 {
		t.Fatalf("prefix matched %d, want 0 — a %% pattern must anchor", prefix.Matched)
	}

	// And with no % at all it stays a substring, which is what most rules want.
	sub, err := dr.AssignByMatch(ctx, MatchRule{
		Field: "alias", Op: "like", Value: "WAN", Customer: "Acme Ltd",
	})
	if err != nil {
		t.Fatalf("substring: %v", err)
	}
	if sub.Matched == 0 {
		t.Fatal("a value with no wildcard stopped behaving as contains")
	}
}

// A bare wildcard would assign every untagged port in the fleet. It is one
// keystroke from a real rule, so it has to be refused rather than previewed.
func TestAssignByMatchRefusesAValueOfOnlyWildcards(t *testing.T) {
	dr, _, _ := seedMatchFixture(t)
	ctx := t.Context()

	for _, v := range []string{"%", "%%", " % ", "%_%"} {
		if _, err := dr.AssignByMatch(ctx, MatchRule{
			Field: "alias", Op: "like", Value: v, Customer: "Acme Ltd",
		}); err == nil {
			t.Fatalf("value %q was accepted", v)
		}
	}
}
