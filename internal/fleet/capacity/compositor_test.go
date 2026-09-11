package capacity

// Purpose (this file): table-driven unit tests for Compositor covering
// task 6's required cases: all-sources-present, per-source stale-TTL
// expiry, verbatim presence readback from DeviceRecord.Presence
// (P1-E36-W7-S72-T2's real nodes.Prober/AdvancePresence output, not a
// value re-derived here), and nil/erroring source paths. Every instant is
// injected via fakeClock; no bare time.Now, no -race unsafe access
// (Compositor's own mutex guards every call).

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/providers/registry"
)

type fakeClock struct{ t time.Time }

func (f *fakeClock) Now() time.Time          { return f.t }
func (f *fakeClock) Advance(d time.Duration) { f.t = f.t.Add(d) }

type fakeProviderSource struct {
	providers []registry.ProviderRecord
	lanes     []registry.LaneRecord
	err       error
}

func (f *fakeProviderSource) ListProviders(context.Context) ([]registry.ProviderRecord, error) {
	return f.providers, f.err
}

func (f *fakeProviderSource) ListLanes(context.Context) ([]registry.LaneRecord, error) {
	return f.lanes, f.err
}

type fakeNodeSource struct {
	devices []nodes.DeviceRecord
	err     error
}

func (f *fakeNodeSource) List() ([]nodes.DeviceRecord, error) { return f.devices, f.err }

// newAllSourcesCompositor builds a Compositor with a provider (two lanes,
// one bucket absent) and one reachable node already applied, plus a
// sampler reading for the self node - the fixture TestFleetSnapshotCompositor
// and TestFleetSnapshotStale both start from.
func newAllSourcesCompositor(t *testing.T, clk *fakeClock) *Compositor {
	t.Helper()
	comp := NewCompositor(clk, time.Hour, "node1")
	providerSrc := &fakeProviderSource{
		providers: []registry.ProviderRecord{{Name: "acme"}},
		lanes: []registry.LaneRecord{
			{ProviderName: "acme", LaneName: "l1", Capacity: BucketInteractiveUsage, State: registry.LaneStateAvailable},
			{ProviderName: "acme", LaneName: "l2", Capacity: BucketAgentSDKCredit, State: registry.LaneStateConstrained},
		},
	}
	nodeSrc := &fakeNodeSource{devices: []nodes.DeviceRecord{
		{NodeID: "node1", Tier: nodes.TierWorkerTrusted, LastSeen: clk.t, Presence: nodes.PresenceReachable},
	}}
	if err := comp.UpdateProviders(context.Background(), providerSrc); err != nil {
		t.Fatalf("UpdateProviders: %v", err)
	}
	if err := comp.UpdateNodes(context.Background(), nodeSrc); err != nil {
		t.Fatalf("UpdateNodes: %v", err)
	}
	comp.UpdateSampler(governor.ResourceSnapshot{CPUFraction: 0.42, SampledAt: clk.t})
	return comp
}

func assertAllSourcesSnapshot(t *testing.T, snap FleetSnapshot) {
	t.Helper()
	acme, ok := snap.Providers["acme"]
	if !ok {
		t.Fatalf("provider slot %q not populated", "acme")
	}
	if got := acme.Buckets[BucketInteractiveUsage].State; got != registry.LaneStateAvailable {
		t.Errorf("interactive_usage state = %q, want available", got)
	}
	if got := acme.Buckets[BucketAgentSDKCredit].State; got != registry.LaneStateConstrained {
		t.Errorf("agent_sdk_credit state = %q, want constrained", got)
	}
	if got := acme.Buckets[BucketAPICredit].State; got != StateUnknown {
		t.Errorf("api_credit state = %q, want unknown (no lane data)", got)
	}
	if got := acme.State; got != registry.LaneStateConstrained {
		t.Errorf("provider aggregate state = %q, want constrained (worst of available/constrained/unknown)", got)
	}

	node1, ok := snap.Nodes["node1"]
	if !ok {
		t.Fatalf("node slot %q not populated", "node1")
	}
	if node1.Presence != PresenceReachable {
		t.Errorf("presence = %q, want reachable", node1.Presence)
	}
	if node1.TrustTier != string(nodes.TierWorkerTrusted) {
		t.Errorf("trust_tier = %q, want %q", node1.TrustTier, nodes.TierWorkerTrusted)
	}
	if node1.Load != 0.42 {
		t.Errorf("load = %v, want 0.42 (from sampler)", node1.Load)
	}
	if node1.Diagnostic == "" {
		t.Error("expected a hardware-inventory diagnostic (no census source exists)")
	}
}

func TestFleetSnapshotCompositor(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)}
	comp := newAllSourcesCompositor(t, clk)
	assertAllSourcesSnapshot(t, comp.Snapshot())
}

func TestFleetSnapshotStale(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)}
	comp := NewCompositor(clk, time.Minute, "")

	providerSrc := &fakeProviderSource{
		providers: []registry.ProviderRecord{{Name: "acme"}},
		lanes:     []registry.LaneRecord{{ProviderName: "acme", Capacity: BucketInteractiveUsage, State: registry.LaneStateAvailable}},
	}
	nodeSrc := &fakeNodeSource{devices: []nodes.DeviceRecord{{NodeID: "node1", LastSeen: clk.t, Presence: nodes.PresenceReachable}}}

	if err := comp.UpdateProviders(context.Background(), providerSrc); err != nil {
		t.Fatalf("UpdateProviders: %v", err)
	}
	if err := comp.UpdateNodes(context.Background(), nodeSrc); err != nil {
		t.Fatalf("UpdateNodes: %v", err)
	}

	clk.Advance(2 * time.Minute) // past the 1-minute ttl

	snap := comp.Snapshot()
	if got := snap.Providers["acme"].State; got != StateUnknown {
		t.Errorf("stale provider state = %q, want unknown", got)
	}
	if got := snap.Providers["acme"].Buckets[BucketInteractiveUsage].State; got != StateUnknown {
		t.Errorf("stale bucket state = %q, want unknown", got)
	}
	if got := snap.Nodes["node1"].Presence; got != PresenceUnknown {
		t.Errorf("stale node presence = %q, want unknown", got)
	}
}

// TestNodePresenceReadVerbatimFromRecord proves the P1-E36-W7-S72-T2
// migration end to end: buildNodeSlot no longer re-derives Presence from
// LastSeen/a local miss counter (that stopgap is retired) - it reads
// exactly what nodes.Prober/nodes.AdvancePresence already persisted onto
// DeviceRecord.Presence, whatever value that is, including the fail-closed
// default for a record neither has touched yet.
func TestNodePresenceReadVerbatimFromRecord(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)}
	comp := NewCompositor(clk, time.Hour, "")

	cases := []struct {
		name string
		rec  nodes.DeviceRecord
		want PresenceState
	}{
		{"unavailable (three real probe misses, real hysteresis)", withThreeMisses(clk.t), presenceUnavailable},
		{"reachable (real prober already resolved it)", nodes.DeviceRecord{NodeID: "node1", Presence: nodes.PresenceReachable}, PresenceReachable},
		{"remote-via-route", nodes.DeviceRecord{NodeID: "node1", Presence: nodes.PresenceRemoteViaRoute}, presenceRemoteViaRoute},
		{"never touched by the prober yet", nodes.DeviceRecord{NodeID: "node1"}, PresenceUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nodeSrc := &fakeNodeSource{devices: []nodes.DeviceRecord{tc.rec}}
			if err := comp.UpdateNodes(context.Background(), nodeSrc); err != nil {
				t.Fatalf("UpdateNodes: %v", err)
			}
			if got := comp.Snapshot().Nodes["node1"].Presence; got != tc.want {
				t.Errorf("presence = %q, want %q", got, tc.want)
			}
		})
	}
}

// withThreeMisses runs nodes.AdvancePresence three real times (the actual
// hysteresis state machine P1-E36-W7-S72-T2 landed), producing a
// DeviceRecord identical to what nodes.Prober would have persisted after
// three consecutive direct-probe misses - never a value this test
// fabricates by setting the field directly.
func withThreeMisses(base time.Time) nodes.DeviceRecord {
	rec := nodes.DeviceRecord{NodeID: "node1", Presence: nodes.PresenceReachable}
	now := base
	for i := 0; i < nodes.ProbeMissThreshold; i++ {
		now = now.Add(15 * time.Second)
		rec, _ = nodes.AdvancePresence(rec, nodes.PresenceObservation{OK: false, At: now, Authenticated: true}, now)
	}
	return rec
}

func TestCompositorNilSourceErrors(t *testing.T) {
	clk := &fakeClock{t: time.Now()}
	comp := NewCompositor(clk, time.Hour, "")
	wantErr := context.DeadlineExceeded

	if err := comp.UpdateProviders(context.Background(), &fakeProviderSource{err: wantErr}); err != wantErr {
		t.Errorf("UpdateProviders error = %v, want %v", err, wantErr)
	}
	if err := comp.UpdateNodes(context.Background(), &fakeNodeSource{err: wantErr}); err != wantErr {
		t.Errorf("UpdateNodes error = %v, want %v", err, wantErr)
	}
	// A failed Update must never panic on the read side either.
	_ = comp.Snapshot()
}
