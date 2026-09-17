package nodes

import (
	"strings"
	"testing"
)

// TestResolveSensitivityFailsClosed pins the rule that decides where
// unclassifiable work runs. Every unrecognized spelling — including the
// empty string, which is what a zero-value Requirement carries — must
// resolve to local-only, because guessing the other way sends work the
// system could not classify onto a remote machine.
func TestResolveSensitivityFailsClosed(t *testing.T) {
	known := map[string]Sensitivity{
		"local-only": SensitivityLocalOnly,
		"restricted": SensitivityRestricted,
		"normal":     SensitivityNormal,
	}
	for raw, want := range known {
		if got := ResolveSensitivity(raw); got != want {
			t.Errorf("ResolveSensitivity(%q) = %q, want %q", raw, got, want)
		}
	}

	for _, raw := range []string{"", " ", "Normal", "NORMAL", "public", "internal", "unknown", "0"} {
		if got := ResolveSensitivity(raw); got != SensitivityLocalOnly {
			t.Errorf("ResolveSensitivity(%q) = %q, want it to fail closed to %q", raw, got, SensitivityLocalOnly)
		}
	}
}

// TestLocalOnlyWorkExcludesEveryTier is the property that distinguishes
// this filter from trust.go's GateLocalOnly.
//
// GateLocalOnly is an identity check against TierController, and that is
// correct for asking "is this machine the controller". Here every candidate
// is an ENROLLED node, which is by definition not the controller machine
// running the engine, whatever its stored tier string says. A record
// carrying tier "controller" must therefore still be excluded — otherwise a
// mislabelled or tampered record routes local-only work off the box.
func TestLocalOnlyWorkExcludesEveryTier(t *testing.T) {
	for _, tier := range []Tier{TierController, TierWorkerTrusted, TierPairedDevice, "", "made-up"} {
		reason, detail, excluded := excludedByTrust(SensitivityLocalOnly, tier)
		if !excluded {
			t.Errorf("local-only work was allowed on a node with tier %q", tier)
			continue
		}
		if reason != ReasonLocalOnlyWork {
			t.Errorf("tier %q: reason = %q, want %q", tier, reason, ReasonLocalOnlyWork)
		}
		if !strings.Contains(detail, "controller machine") {
			t.Errorf("tier %q: detail = %q, want it to say where the work must run instead", tier, detail)
		}
	}
}

// TestRestrictedWorkNeedsWorkerTrusted pins the ordered-tier rule in both
// directions. Asserting only the refusal would pass for an implementation
// that refused everything.
func TestRestrictedWorkNeedsWorkerTrusted(t *testing.T) {
	allowed := []Tier{TierWorkerTrusted, TierController}
	refused := []Tier{TierPairedDevice, "", "made-up"}

	for _, tier := range allowed {
		if _, _, excluded := excludedByTrust(SensitivityRestricted, tier); excluded {
			t.Errorf("restricted work was refused on tier %q, which outranks worker-trusted", tier)
		}
	}
	for _, tier := range refused {
		reason, detail, excluded := excludedByTrust(SensitivityRestricted, tier)
		if !excluded {
			t.Errorf("restricted work was allowed on tier %q", tier)
			continue
		}
		if reason != ReasonTierTooLow {
			t.Errorf("tier %q: reason = %q, want %q", tier, reason, ReasonTierTooLow)
		}
		if !strings.Contains(detail, "worker-trusted") {
			t.Errorf("tier %q: detail = %q, want it to name the tier required", tier, detail)
		}
	}
}

// TestNormalWorkStillRefusesAnUnrecognizedTier proves the permissive
// sensitivity is not a bypass: a record carrying a tier this build does not
// know is a record whose authorization cannot be reasoned about, so it is
// refused even for work with no tier rule of its own.
func TestNormalWorkStillRefusesAnUnrecognizedTier(t *testing.T) {
	for _, tier := range []Tier{TierController, TierWorkerTrusted, TierPairedDevice} {
		if _, _, excluded := excludedByTrust(SensitivityNormal, tier); excluded {
			t.Errorf("normal work was refused on the recognized tier %q", tier)
		}
	}
	for _, tier := range []Tier{"", "made-up", "Controller"} {
		reason, detail, excluded := excludedByTrust(SensitivityNormal, tier)
		if !excluded {
			t.Errorf("normal work was allowed on the unrecognized tier %q", tier)
			continue
		}
		if reason != ReasonTierTooLow {
			t.Errorf("tier %q: reason = %q, want %q", tier, reason, ReasonTierTooLow)
		}
		if !strings.Contains(detail, "recognized") {
			t.Errorf("tier %q: detail = %q, want it to say the tier is unrecognized", tier, detail)
		}
	}
}

// TestTierNameRendersTheUnsetTier keeps the refusal message readable for
// the commonest bad value: a record with no tier at all.
func TestTierNameRendersTheUnsetTier(t *testing.T) {
	if got := tierName(""); got != "(unset)" {
		t.Fatalf("tierName(\"\") = %q, want an explicit rendering", got)
	}
	if got := tierName(TierWorkerTrusted); got != "worker-trusted" {
		t.Fatalf("tierName(worker-trusted) = %q", got)
	}
}

// TestPlacementPairedDeviceFailsRestricted is R-21.220 asserted through
// the real engine: the trust filter compares the ORDERED rank
// (controller=2 > worker-trusted=1 > paired-device=0) against the gate
// `rank(tier) >= rank(worker-trusted)`, so a paired device is never
// eligible for restricted work no matter what it advertises.
//
// The paired device here reports the required capability and is otherwise
// perfect; the worker-trusted node beside it is identical but for its
// tier. An implementation that ranked by declaration order, or that
// treated an unranked tier as zero, would place the wrong one.
func TestPlacementPairedDeviceFailsRestricted(t *testing.T) {
	req := Requirement{Capabilities: []string{"browser"}, Sensitivity: SensitivityRestricted}
	node := func(id string, tier Tier) Candidate {
		return Candidate{
			Record: DeviceRecord{NodeID: id, Tier: tier, Presence: PresenceReachable},
			Report: CapabilityReport{Capabilities: []string{"browser"}},
		}
	}
	engine := Engine{Tunnels: func(string) TunnelState { return TunnelUp }}

	eligible, err := engine.Eligible(req, []Candidate{node("paired", TierPairedDevice), node("worker", TierWorkerTrusted)})
	if err != nil {
		t.Fatalf("restricted work found no home beside a worker-trusted node: %v", err)
	}
	if len(eligible) != 1 || eligible[0].NodeID != "worker" {
		t.Fatalf("eligible = %+v, want exactly the worker-trusted node", eligible)
	}

	if _, err := engine.Eligible(req, []Candidate{node("paired", TierPairedDevice)}); err == nil {
		t.Fatal("restricted work was placed on a paired device")
	}
}
