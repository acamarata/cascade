package policy

import (
	"strings"
	"testing"
)

// specLadderVerbatim is copied verbatim from 06-FORGE-SPEC.md's numbered
// item 15 (the ticket's "§5.15"). It is a SECOND, independent
// transcription of the ladder: prose, split and translated below by code
// that never reads the enum it checks. A rung mislabelled, misordered or
// given the wrong default disposition in types.go therefore fails here,
// where a table compared against itself would have stayed green.
const specLadderVerbatim = `L0 read-only → allow · ` +
	`L1 safe local dev (tests/lint/build) → allow · ` +
	`L2 workspace mutation → ask · ` +
	`L3 external side effect (push/PR/network/messages) → ask, never auto · ` +
	`L4 destructive/privileged → deny-list, same-turn authorization only`

// specFailClosedVerbatim is the §5.15 fail-closed sentence, likewise
// verbatim.
const specFailClosedVerbatim = `the level enum has NO permissive zero-value — ` +
	`unknown/unclassifiable ⇒ L4`

// specDescriptionToClass bridges the ladder's prose description to the
// R-16.46 ActionClass spelling. This map is the ONLY place the two
// vocabularies meet; neither side is derived from the other.
var specDescriptionToClass = map[string]ActionClass{
	"read-only":              ClassRead,
	"safe local dev":         ClassLocalDev,
	"workspace mutation":     ClassWorkspaceMutation,
	"external side effect":   ClassExternalSideEffect,
	"destructive/privileged": ClassDestructivePrivileged,
}

// specRung is one parsed clause of specLadderVerbatim.
type specRung struct {
	name        string
	description string
	disposition string
}

// parseSpecLadder splits the verbatim prose into rungs. It knows nothing
// about RiskLevel; it only reads the sentence.
func parseSpecLadder(t *testing.T) []specRung {
	t.Helper()
	var out []specRung
	for _, clause := range strings.Split(specLadderVerbatim, " · ") {
		name, rest, ok := strings.Cut(clause, " ")
		if !ok {
			t.Fatalf("ladder clause %q has no rung name", clause)
		}
		description, disposition, ok := strings.Cut(rest, " → ")
		if !ok {
			t.Fatalf("ladder clause %q has no disposition", clause)
		}
		// Strip the parenthetical example list and the qualifiers that
		// follow the disposition word.
		if idx := strings.Index(description, " ("); idx >= 0 {
			description = description[:idx]
		}
		disposition, _, _ = strings.Cut(disposition, ",")
		disposition, _, _ = strings.Cut(disposition, "-")
		out = append(out, specRung{name: name, description: description, disposition: disposition})
	}
	return out
}

func TestRiskLadderMatchesSpec(t *testing.T) {
	rungs := parseSpecLadder(t)
	if len(rungs) != 5 {
		t.Fatalf("spec ladder has %d rungs, expected 5 (drift in specLadderVerbatim itself)", len(rungs))
	}
	var previous RiskLevel
	for i, rung := range rungs {
		class, ok := specDescriptionToClass[rung.description]
		if !ok {
			t.Fatalf("no ActionClass translation registered for spec description %q", rung.description)
		}
		level := class.Risk()
		if got := level.String(); got != rung.name {
			t.Errorf("spec rung %d is %q but %s maps to %s", i, rung.name, class, got)
		}
		if got := level.disposition(); got != rung.disposition {
			t.Errorf("%s default disposition = %q, spec says %q", level, got, rung.disposition)
		}
		if i > 0 && level <= previous {
			t.Errorf("%s is not more restrictive than the rung before it; ladder order is load-bearing", level)
		}
		previous = level
	}
}

func TestRiskLevelHasNoPermissiveZeroValue(t *testing.T) {
	if !strings.Contains(specFailClosedVerbatim, "NO permissive zero-value") {
		t.Fatal("specFailClosedVerbatim no longer states the zero-value rule")
	}
	if RiskLevel(0).Valid() {
		t.Fatal("RiskLevel(0) must not be a valid rung")
	}
	if got := safeLevel(RiskLevel(0)); got != L4 {
		t.Fatalf("safeLevel(RiskLevel(0)) = %s, want L4", got)
	}
	for _, bad := range []RiskLevel{0, 6, 7, 255} {
		if got := safeLevel(bad); got != L4 {
			t.Errorf("safeLevel(%d) = %s, want L4", bad, got)
		}
		if got := RiskLevel(bad).String(); got != "invalid-risk-level" {
			t.Errorf("RiskLevel(%d).String() = %q, want invalid-risk-level", bad, got)
		}
		if got := RiskLevel(bad).disposition(); got != "deny" {
			t.Errorf("RiskLevel(%d).disposition() = %q, want deny", bad, got)
		}
	}
}

func TestRiskLevelNumbering(t *testing.T) {
	// The contract fixes the iota so that no field left unset reads as L0.
	for i, want := range []RiskLevel{L0, L1, L2, L3, L4} {
		if int(want) != i+1 {
			t.Errorf("%s = %d, want %d (iota starts at 1)", want, want, i+1)
		}
		if !want.Valid() {
			t.Errorf("%s must be valid", want)
		}
	}
}

func TestMaxLevel(t *testing.T) {
	cases := []struct {
		a, b, want RiskLevel
	}{
		{L0, L0, L0},
		{L0, L3, L3},
		{L3, L0, L3},
		{L4, L1, L4},
		{L1, L4, L4},
		{0, L0, L4}, // an invalid operand cannot lower the result
		{L0, 0, L4}, // and cannot in either position
		{0, 0, L4},  // nor when both are invalid
	}
	for _, tc := range cases {
		if got := maxLevel(tc.a, tc.b); got != tc.want {
			t.Errorf("maxLevel(%d, %d) = %s, want %s", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestActionClassEnum(t *testing.T) {
	names := map[ActionClass]string{
		ClassRead:                  "read",
		ClassLocalDev:              "local_dev",
		ClassWorkspaceMutation:     "workspace_mutation",
		ClassExternalSideEffect:    "external_side_effect",
		ClassDestructivePrivileged: "destructive_privileged",
	}
	seen := map[RiskLevel]bool{}
	for class, name := range names {
		if got := class.String(); got != name {
			t.Errorf("ActionClass(%d).String() = %q, want %q", class, got, name)
		}
		if !class.Valid() {
			t.Errorf("%s must be valid", name)
		}
		level := class.Risk()
		if seen[level] {
			t.Errorf("%s maps to %s, which another class already claims; the mapping must be one to one", name, level)
		}
		seen[level] = true
	}
	if len(seen) != 5 {
		t.Fatalf("the five classes cover %d rungs, want 5", len(seen))
	}
}

func TestActionClassZeroValueIsNotPermissive(t *testing.T) {
	for _, bad := range []ActionClass{0, 6, 200} {
		if bad.Valid() {
			t.Errorf("ActionClass(%d) must not be valid", bad)
		}
		if got := bad.Risk(); got != L4 {
			t.Errorf("ActionClass(%d).Risk() = %s, want L4", bad, got)
		}
		if got := bad.String(); got != "invalid-action-class" {
			t.Errorf("ActionClass(%d).String() = %q, want invalid-action-class", bad, got)
		}
	}
}
