package nodes

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// TestResolveSensitivityFailsClosed pins the rule that decides where
// unclassifiable work runs. Every unrecognized spelling — including the
// empty string, which is what a zero-value Requirement carries — must
// resolve to local-only, because guessing the other way sends work the
// system could not classify onto a remote machine.
func TestResolveSensitivityFailsClosed(t *testing.T) {
	known := map[string]provider.SensitivityTier{
		"local-only": provider.SensitivityLocalOnly,
		"restricted": provider.SensitivityRestricted,
		"normal":     provider.SensitivityInternal,
	}
	for raw, want := range known {
		if got := decodeWireSensitivity(raw); got != want {
			t.Errorf("decodeWireSensitivity(%q) = %q, want %q", raw, got, want)
		}
	}

	for _, raw := range []string{"", " ", "Normal", "NORMAL", "public", "internal", "unknown", "0"} {
		if got := decodeWireSensitivity(raw); got != provider.SensitivityLocalOnly {
			t.Errorf("decodeWireSensitivity(%q) = %q, want it to fail closed to %q", raw, got, provider.SensitivityLocalOnly)
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
		reason, detail, excluded := excludedByTrust(provider.SensitivityLocalOnly, tier)
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
		if _, _, excluded := excludedByTrust(provider.SensitivityRestricted, tier); excluded {
			t.Errorf("restricted work was refused on tier %q, which outranks worker-trusted", tier)
		}
	}
	for _, tier := range refused {
		reason, detail, excluded := excludedByTrust(provider.SensitivityRestricted, tier)
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
		if _, _, excluded := excludedByTrust(provider.SensitivityInternal, tier); excluded {
			t.Errorf("normal work was refused on the recognized tier %q", tier)
		}
	}
	for _, tier := range []Tier{"", "made-up", "Controller"} {
		reason, detail, excluded := excludedByTrust(provider.SensitivityInternal, tier)
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
	req := Requirement{Capabilities: []string{"browser"}, Sensitivity: provider.SensitivityRestricted}
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

// TestFormerParseSitesFailClosed is this package's row of the former-parse-
// site table: the node.dispatch wire decode. An unknown and an empty wire
// value decode to local-only, and local-only work is excluded from every
// candidate, so the most restrictive outcome holds end to end.
func TestFormerParseSitesFailClosed(t *testing.T) {
	t.Run("nodes_wire_decode", func(t *testing.T) {
		for _, raw := range []string{"", "secret", "RESTRICTED ", "internal", "public"} {
			got := decodeWireSensitivity(raw)
			if got != provider.SensitivityLocalOnly {
				t.Fatalf("decodeWireSensitivity(%q) = %v, want local-only", raw, got)
			}
			if _, _, excluded := excludedByTrust(got, TierWorkerTrusted); !excluded {
				t.Fatalf("wire %q reached a worker-trusted node", raw)
			}
		}
	})
}

// persistedDispatch is json.Marshal(DispatchRequest{...}) at f688c0b with
// the given wire sensitivity (evidence run4/fixtures-provenance.txt).
func persistedDispatch(wire string) string {
	return `{"dispatch_id":"d1","node_id":"n1","head":"abc","action_id":"a1","idempotent":true,"sensitivity":"` +
		wire + `","credential":"none"}`
}

// TestPersistedSensitivityFormsUnchanged is this package's persisted-form
// row: node.dispatch requests in each f688c0b wire spelling, "normal"
// included, decode to the right tier, re-encode through the wire codec,
// and marshal back byte-identical.
func TestPersistedSensitivityFormsUnchanged(t *testing.T) {
	for wire, want := range map[string]provider.SensitivityTier{
		"local-only": provider.SensitivityLocalOnly,
		"restricted": provider.SensitivityRestricted,
		"normal":     provider.SensitivityInternal,
	} {
		raw := persistedDispatch(wire)
		req, err := decodeDispatchRequest(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		tier := decodeWireSensitivity(req.Sensitivity)
		if tier != want {
			t.Fatalf("wire %q decoded to %v, want %v", wire, tier, want)
		}
		req.Sensitivity = encodeWireSensitivity(tier)
		out, err := json.Marshal(req)
		if err != nil || string(out) != raw {
			t.Fatalf("re-encode:\n got %s (%v)\nwant %s", out, err, raw)
		}
	}
	if got := encodeWireSensitivity(provider.SensitivityPublic); got != "normal" {
		t.Fatalf("public encodes as %q, want normal", got)
	}
	if got := encodeWireSensitivity(provider.SensitivityTier(9)); got != "local-only" {
		t.Fatalf("an out-of-range tier encodes as %q, want local-only", got)
	}
}

// TestTierOutOfRangeRefused is this package's [R115b] row: a value above
// SensitivityPublic is excluded from every candidate, including the
// controller-tier record that clears every gate, exactly like local-only
// work. Removing the default branch's exclusion turns it red.
func TestTierOutOfRangeRefused(t *testing.T) {
	for _, tier := range []Tier{TierController, TierWorkerTrusted, TierPairedDevice} {
		reason, detail, excluded := excludedByTrust(provider.SensitivityTier(9), tier)
		if !excluded || reason != ReasonLocalOnlyWork {
			t.Fatalf("sensitivity 9 on %q: excluded=%v reason=%q, want excluded as local-only", tier, excluded, reason)
		}
		if !strings.Contains(detail, "is not a tier") {
			t.Fatalf("detail %q must say the value is not a tier", detail)
		}
	}
}
