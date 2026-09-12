// Purpose: TestProbeAdmissionBackoff (1m->1h doubling, classification to
// exhausted|auth_required) and TestSnapshotStalenessDegradesToUnknown
// (R-21.109 per-source expiry) -- the two named acceptance tests for this
// file, plus TryAdmit's in-flight/backoff gating.
//
// SPORT: fleet.capacity.probe (ADD, P1-E31-W6-S63-T2).
package capacity

import (
	"testing"
	"time"
)

// fakeClock (compositor_test.go, this package) is reused as the
// deterministic, mutable Clock for these tests -- never a bare time.Now.

// TestSnapshotStalenessDegradesToUnknown asserts R-21.109's per-source
// expiry: provider-status 10m, cli-observation 30m, user-estimate 24h.
func TestSnapshotStalenessDegradesToUnknown(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		src  ObservationSource
		age  time.Duration
		want State
	}{
		{"provider-status fresh", SourceProviderStatus, 9 * time.Minute, StateAvailable},
		{"provider-status stale", SourceProviderStatus, 11 * time.Minute, StateUnknown},
		{"cli-observation fresh", SourceCLIObservation, 29 * time.Minute, StateAvailable},
		{"cli-observation stale", SourceCLIObservation, 31 * time.Minute, StateUnknown},
		{"user-estimate fresh", SourceUserEstimate, 23 * time.Hour, StateAvailable},
		{"user-estimate stale", SourceUserEstimate, 25 * time.Hour, StateUnknown},
		{"unknown source fails closed immediately", ObservationSource("bogus"), time.Nanosecond, StateUnknown},
		{"non-available state unaffected", SourceProviderStatus, 100 * time.Hour, StateConstrained},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := StateAvailable
			if c.want == StateConstrained {
				raw = StateConstrained
			}
			got := DegradeStaleness(raw, c.src, base, base.Add(c.age))
			if got != c.want {
				t.Fatalf("DegradeStaleness(%v, %v, age=%v) = %v, want %v", raw, c.src, c.age, got, c.want)
			}
		})
	}
}

// TestProbeAdmissionBackoff asserts the 1m -> 2m -> 4m -> 8m -> 16m ->
// 32m -> 1h(capped) doubling schedule and that a successful classify
// clears it.
func TestProbeAdmissionBackoff(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	a := NewDefaultProbeAdmission()

	wantSteps := []time.Duration{
		time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute,
		16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour,
	}
	for i, want := range wantSteps {
		if !a.TryAdmit("p1", true, clock.t) {
			t.Fatalf("step %d: TryAdmit at start of window = false, want true", i)
		}
		a.Classify("p1", StateExhausted, clock.t)
		// immediately inside the new backoff window: refused.
		if a.TryAdmit("p1", true, clock.t.Add(time.Second)) {
			t.Fatalf("step %d: TryAdmit inside backoff = true, want false", i)
		}
		// one second short of expiry: still refused.
		if a.TryAdmit("p1", true, clock.t.Add(want-time.Second)) {
			t.Fatalf("step %d: TryAdmit one second before expiry (%v) = true, want false", i, want)
		}
		// exactly at expiry: admitted again -- advance the real clock there.
		clock.t = clock.t.Add(want)
	}

	// auth_required classifies distinctly but shares the same backoff ladder.
	clock.t = clock.t.Add(time.Hour)
	if !a.TryAdmit("p2", true, clock.t) {
		t.Fatal("TryAdmit(p2) = false, want true (fresh profile)")
	}
	a.Classify("p2", StateAuthRequired, clock.t)
	if a.TryAdmit("p2", true, clock.t) {
		t.Fatal("TryAdmit(p2) immediately after auth_required classify = true, want false")
	}

	// a success clears the backoff entirely.
	clock.t = clock.t.Add(time.Minute)
	if !a.TryAdmit("p3", true, clock.t) {
		t.Fatal("TryAdmit(p3) = false, want true (fresh profile)")
	}
	a.Classify("p3", StateAvailable, clock.t)
	if !a.TryAdmit("p3", true, clock.t) {
		t.Fatal("TryAdmit(p3) after a success classify = false, want true (backoff cleared)")
	}
}

// TestTierPolicyProbeOnlyUnknown asserts that a concurrent request for
// the same in-flight profile sees the slot as exhausted, never as
// available (R-21.166).
func TestTierPolicyProbeOnlyUnknown(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	a := NewDefaultProbeAdmission()

	if !a.TryAdmit("p1", true, clock.t) {
		t.Fatal("first TryAdmit(dispatchProbe=true) = false, want true")
	}
	if a.TryAdmit("p1", true, clock.t) {
		t.Fatal("second concurrent TryAdmit(dispatchProbe=true) = true, want false (only one in-flight probe)")
	}
	// a non-probe caller (raw state already known) is unaffected by the
	// other profile's in-flight probe -- only the SAME profile is gated.
	if !a.TryAdmit("p2", false, clock.t) {
		t.Fatal("TryAdmit(p2, dispatchProbe=false) = false, want true (different profile)")
	}

	a.Classify("p1", StateExhausted, clock.t)
	// the in-flight probe resolved; a fresh probe is refused only because
	// of the resulting backoff, not because of "in-flight" anymore.
	if a.TryAdmit("p1", true, clock.t) {
		t.Fatal("TryAdmit(p1) immediately after Classify = true, want false (now in backoff)")
	}
}
