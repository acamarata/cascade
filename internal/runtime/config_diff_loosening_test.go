package runtime

// Purpose: tests for the ApplyDiff loosening refusals (config_diff.go) —
//   the guarded-family and CompareSecurity gates.
// Inputs: n/a (test-only, every write under t.TempDir()).
// Outputs: n/a (test-only).
// Constraints: no network, no real clock; every case is hermetic.
// SPORT: internal/runtime config_diff.go (ADD) — P1-E25-W5-S103-T1.

import (
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestApplyDiffRefusesCompareSecurityLoosening proves both gates. The
// public ApplyDiff refuses a conductor loosening (the guarded-family gate
// fires first). The post-compose CompareSecurity gate is then driven on
// its own through applyVetted, the step after vetting, with a vetted
// conductor entry: every CompareSecurity family is also a guarded family,
// so this is the only way to prove the second gate is live rather than
// dead code (a mutation that disables it turns this test RED).
func TestApplyDiffRefusesCompareSecurityLoosening(t *testing.T) {
	// conductor.external_routing_enabled is ABSENT (default false), so the
	// entry classifies as "absent" and reaches the gate rather than being
	// skipped as user-set.
	const seed = "[runtime]\nprofile = \"local\"\n"
	w := writerAt(t, seed)
	_, err := w.ApplyDiff(ConfigDiff{Owner: "cascade-nself", Entries: []DiffEntry{
		{Path: "conductor.external_routing_enabled", Literal: "true"},
	}})
	assertKind(t, err, cascade.KindPolicyDenied)

	_, err = w.applyVetted("cascade-nself", []vettedEntry{
		{Path: "conductor.external_routing_enabled", Value: true, Canonical: "true"},
	})
	assertKind(t, err, cascade.KindPolicyDenied)
	if !strings.Contains(err.Error(), "conductor.external_routing_enabled") {
		t.Fatalf("err = %v, want it to name the loosened key", err)
	}
	data, rerr := os.ReadFile(w.Path)
	if rerr != nil || string(data) != seed {
		t.Fatalf("a refused loosening wrote the file (%v):\n%s", rerr, data)
	}
}

// TestApplyDiffRefusesTwoFamilyLoosening proves the CompareSecurity gate
// (gateCandidate, config_diff.go) catches a diff that loosens two
// different guarded families in the SAME write, not only a single-family
// loosening (TestApplyDiffRefusesCompareSecurityLoosening above). It
// drives applyVetted directly, exactly as that test does, because every
// CompareSecurity family is also a guarded family: the public ApplyDiff
// entry point would refuse on the first entry at the vetDiff/
// vetPathAuthority stage (guarded-family gate) before CompareSecurity
// ever ran, which would prove nothing about the second gate.
//
// nodes.trust_tier and conductor.spill_enabled are picked because
// CompareSecurity classifies both as loosening by different rules
// (compareAnyChange for trust_tier: no ratified order, any change
// counts; compareBool for spill_enabled: false -> true expands egress),
// so together they exercise two distinct code paths inside the gate.
func TestApplyDiffRefusesTwoFamilyLoosening(t *testing.T) {
	// Guard: prove CompareSecurity itself reports BOTH families as
	// loosening for this exact before/after, so the test below cannot
	// pass vacuously (e.g. because only one of the two entries is ever
	// actually flagged).
	before := EffectiveConfig{}
	after := EffectiveConfig{
		Nodes:     NodesSection{TrustTier: "controller"},
		Conductor: ConductorSection{SpillEnabled: true},
	}
	paths := CompareSecurity(before, after)
	families := map[string]bool{}
	for _, p := range paths {
		families[p.Family] = true
	}
	if len(paths) != 2 || !families["nodes"] || !families["conductor"] {
		t.Fatalf("guard failed: CompareSecurity(before, after) = %+v, want exactly one nodes path and one conductor path", paths)
	}

	// Both paths are ABSENT from the seed, so both entries classify as
	// "absent" and reach the gate rather than being skipped as user-set.
	const seed = "[runtime]\nprofile = \"local\"\n"
	w := writerAt(t, seed)
	_, err := w.applyVetted("cascade-nself", []vettedEntry{
		{Path: "nodes.trust_tier", Value: "controller", Canonical: `"controller"`},
		{Path: "conductor.spill_enabled", Value: true, Canonical: "true"},
	})
	assertKind(t, err, cascade.KindPolicyDenied)
	if !strings.Contains(err.Error(), "nodes.trust_tier") && !strings.Contains(err.Error(), "conductor.spill_enabled") {
		t.Fatalf("err = %v, want it to name at least one of the two loosened keys", err)
	}
	data, rerr := os.ReadFile(w.Path)
	if rerr != nil || string(data) != seed {
		t.Fatalf("a refused two-family loosening wrote the file (%v):\n%s", rerr, data)
	}
}

// TestApplyDiffRefusesLoosenOneFamilyWhileTighteningAnother proves the
// gate refuses a diff that tightens one guarded family while loosening a
// different one in the same write — the loosening in the second family
// must not be masked by the tightening in the first. conductor.
// external_routing_enabled starts true (recorded as this owner's own
// earlier managed write, so a NEW value for it classifies as
// "owner-managed" and is applied rather than skipped as user-set) and
// the diff turns it false, which compareBool does NOT flag (proposed ==
// false is never loosening). nodes.trust_tier is absent and the diff
// sets it, which compareAnyChange DOES flag. The whole write must still
// be refused.
func TestApplyDiffRefusesLoosenOneFamilyWhileTighteningAnother(t *testing.T) {
	// Guard: prove CompareSecurity reports the loosening in nodes but NOT
	// a loosening for the conductor tighten, for this exact before/after.
	before := EffectiveConfig{Conductor: ConductorSection{ExternalRoutingEnabled: true}}
	after := EffectiveConfig{
		Conductor: ConductorSection{ExternalRoutingEnabled: false},
		Nodes:     NodesSection{TrustTier: "controller"},
	}
	paths := CompareSecurity(before, after)
	if len(paths) != 1 || paths[0].Family != "nodes" || paths[0].Key != "nodes.trust_tier" {
		t.Fatalf("guard failed: CompareSecurity(before, after) = %+v, want exactly one nodes.trust_tier path", paths)
	}

	const seed = "[runtime]\nprofile = \"local\"\n\n[conductor]\nexternal_routing_enabled = true\n\n" +
		"[plugins.cascade-nself]\nmanaged = {\"conductor.external_routing_enabled\" = \"true\"}\n"
	w := writerAt(t, seed)
	_, err := w.applyVetted("cascade-nself", []vettedEntry{
		{Path: "conductor.external_routing_enabled", Value: false, Canonical: "false"},
		{Path: "nodes.trust_tier", Value: "controller", Canonical: `"controller"`},
	})
	assertKind(t, err, cascade.KindPolicyDenied)
	if !strings.Contains(err.Error(), "nodes.trust_tier") {
		t.Fatalf("err = %v, want it to name the loosened key nodes.trust_tier", err)
	}
	data, rerr := os.ReadFile(w.Path)
	if rerr != nil || string(data) != seed {
		t.Fatalf("a refused mixed tighten/loosen write wrote the file (%v):\n%s", rerr, data)
	}
}
