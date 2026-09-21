package cascadepa

// Purpose (this file): the verb-candidate mapping and the elevated-verb
//   question, including the CR bypass input `/enroll worker-3` arriving as
//   callback data.
//
// SPORT: plugins/cascade-pa bridge-policy-tests/TEST (P1-E23-W5-S48-T1).

import (
	"strings"
	"testing"
)

// verbSet is an ElevationPolicy over an explicit set.
type verbSet map[string]bool

func (v verbSet) IsElevatedVerb(verb string) bool { return v[verb] }

// nodeVerbs mirrors the subset of the host's §5.14 table this file asks about.
func nodeVerbs() verbSet {
	return verbSet{"node.enroll": true, "node.upgrade": true, "node.remove": true, "vault.get": true}
}

func TestVerbCandidates(t *testing.T) {
	for _, tc := range []struct {
		name  string
		raw   string
		shape bool
		want  []string
	}{
		{"bare enroll alias", "/enroll worker-3", false, []string{"node.enroll", "enroll.worker-3", "enroll"}},
		{"dotted node verb", "/node enroll worker-3", false, []string{"node.enroll", "node"}},
		{"upgrade alias", "/upgrade", false, []string{"node.upgrade", "upgrade"}},
		{"vault get", "/vault get key", false, []string{"vault.get", "vault"}},
		{"callback data", "approve:req-7", true, []string{"approve.req-7", "approve"}},
		{"callback enroll", "enroll:worker-3", true, []string{"node.enroll", "enroll.worker-3", "enroll"}},
		{"prose is not a verb", "hello there", false, nil},
		{"empty", "", false, nil},
		{"slash only", "/", false, nil},
		{"whitespace callback", "   ", true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := VerbCandidates(tc.raw, tc.shape)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("VerbCandidates(%q, %v) = %v, want %v", tc.raw, tc.shape, got, tc.want)
			}
		})
	}
}

// TestRefusesElevated_TheCRBypassInputs are the exact strings the adversarial
// review used, on both transports.
func TestRefusesElevated_TheCRBypassInputs(t *testing.T) {
	policy := nodeVerbs()
	for _, raw := range []string{"/enroll worker-3", "/node enroll worker-3", "/upgrade", "/node upgrade w"} {
		if !RefusesElevated(policy, raw, false) {
			t.Fatalf("text %q was admitted", raw)
		}
		if !RefusesElevated(policy, raw, true) {
			t.Fatalf("callback data %q was admitted", raw)
		}
	}
	// The callback form without the leading slash — the shape a button sends.
	if !RefusesElevated(policy, "enroll:worker-3", true) {
		t.Fatal("callback data \"enroll:worker-3\" was admitted")
	}
}

// TestRefusesElevated_OrdinaryContentIsCarried keeps the refusal from being
// "everything": a bridge that refused prose would carry nothing.
func TestRefusesElevated_OrdinaryContentIsCarried(t *testing.T) {
	policy := nodeVerbs()
	for _, raw := range []string{"hello there", "what is my next task?", "", "  "} {
		if RefusesElevated(policy, raw, false) {
			t.Fatalf("ordinary text %q was refused", raw)
		}
	}
	if RefusesElevated(policy, "approve:req-7", true) {
		t.Fatal("an ordinary approval callback was refused")
	}
}

// TestRefusesElevated_ComesFromThePolicy proves the decision is the injected
// table's: under an empty policy the same command is admitted.
func TestRefusesElevated_ComesFromThePolicy(t *testing.T) {
	if RefusesElevated(verbSet{}, "/enroll worker-3", false) {
		t.Fatal("a command was refused by a policy that classifies nothing as elevated; " +
			"the decision is coming from a hardcoded list")
	}
}

// TestRefusesElevated_NoPolicyRefusesEveryCommand is the fail-closed default.
func TestRefusesElevated_NoPolicyRefusesEveryCommand(t *testing.T) {
	if !RefusesElevated(nil, "/status", false) {
		t.Fatal("a command was admitted with no elevation policy wired")
	}
	if !RefusesElevated(nil, "approve:req-7", true) {
		t.Fatal("callback data was admitted with no elevation policy wired")
	}
	if RefusesElevated(nil, "ordinary prose", false) {
		t.Fatal("prose was refused; an unwired policy must still carry chat content")
	}
}

func TestElevationOrRefuseAll(t *testing.T) {
	policy := nodeVerbs()
	if got := ElevationOrRefuseAll(policy); got.IsElevatedVerb("node.enroll") != true {
		t.Fatal("a real policy was replaced")
	}
	if !ElevationOrRefuseAll(nil).IsElevatedVerb("anything.at.all") {
		t.Fatal("the nil default admitted a verb")
	}
}

// TestSensitivityTierValuesMatchTheEgressStrings pins the plugin-facing tier
// strings to internal/hooks/egress's own values. The host adapter is a cast, so
// a drifted string here would silently classify every reply as an unrecognised
// tier — which the matrix resolves to restricted and REFUSES, taking the whole
// bridge down rather than leaking. The pin makes that a test failure instead.
func TestSensitivityTierValuesMatchTheEgressStrings(t *testing.T) {
	for tier, want := range map[SensitivityTier]string{
		TierLocalOnly:  "local-only",
		TierRestricted: "restricted",
		TierInternal:   "internal",
		TierPublic:     "public",
	} {
		if string(tier) != want {
			t.Fatalf("tier %q must equal %q (internal/hooks/egress/class.go)", string(tier), want)
		}
	}
}
