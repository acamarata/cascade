package nodes

// Purpose: the capability report's journey from a verified heartbeat frame
//   onto the device record placement reads it from (P1-E17-W4-S37-T1).
// Constraints: split out of heartbeat_test.go to stay under Art.10.3's
//   300-line cap. Both halves matter: the report is retained, and it is
//   retained ONLY after verification.

import (
	"context"
	"testing"
)

// TestVerifiedHeartbeatRetainsTheCapabilityReport pins the placement
// engine's capability INPUT. Before P1-E17-W4-S37-T1 the report was
// validated on decode and then dropped on the floor, which left placement
// deciding over records that advertised nothing: every capability
// requirement refused, for a reason no operator could have diagnosed.
func TestVerifiedHeartbeatRetainsTheCapabilityReport(t *testing.T) {
	deps, rec, _, build := newHeartbeatHarness(t)
	if _, err := ProcessHeartbeat(context.Background(), deps, build(1)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stored, err := deps.Records.Get(rec.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range validReport().Capabilities {
		if !HasCapability(stored.LastReport, capability) {
			t.Fatalf("stored report %+v is missing %q", stored.LastReport, capability)
		}
	}
}

// TestAnUnverifiedHeartbeatNeverReachesTheRecord is the other half, and
// the one that matters for authorization: the report is retained only
// AFTER VerifyHeartbeatFrame passes. A replayed sequence is the cheapest
// way to reach ProcessHeartbeat with a frame that decodes and validates
// but does not verify.
func TestAnUnverifiedHeartbeatNeverReachesTheRecord(t *testing.T) {
	deps, rec, _, build := newHeartbeatHarness(t)
	if _, err := ProcessHeartbeat(context.Background(), deps, build(2)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	replay := build(1)
	replay.Report = CapabilityReport{Capabilities: []string{"forged-capability"}}
	if _, err := ProcessHeartbeat(context.Background(), deps, replay); err == nil {
		t.Fatal("a replayed heartbeat was accepted")
	}
	stored, err := deps.Records.Get(rec.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if HasCapability(stored.LastReport, "forged-capability") {
		t.Fatalf("an unverified report reached the record: %+v", stored.LastReport)
	}
}
