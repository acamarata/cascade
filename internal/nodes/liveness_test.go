package nodes

import (
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
)

func TestComputeLivenessNeverHeartbeated(t *testing.T) {
	rec := DeviceRecord{NodeID: "n1"}
	if got := ComputeLiveness(rec, time.Now(), time.Minute); got != LivenessUnknown {
		t.Fatalf("got %q, want LivenessUnknown", got)
	}
}

func TestLivenessUnknownOnTimeout(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rec := DeviceRecord{NodeID: "n1", LastSeen: base}
	// exactly at the timeout boundary is still fresh
	if got := ComputeLiveness(rec, base.Add(time.Minute), time.Minute); got != LivenessReachable {
		t.Fatalf("at boundary: got %q, want LivenessReachable", got)
	}
	// one tick past the timeout: fail closed to unknown, never a stale
	// "reachable".
	if got := ComputeLiveness(rec, base.Add(time.Minute+time.Nanosecond), time.Minute); got != LivenessUnknown {
		t.Fatalf("past boundary: got %q, want LivenessUnknown", got)
	}
}

func TestComputeLivenessReachableWithinWindow(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rec := DeviceRecord{NodeID: "n1", LastSeen: base}
	if got := ComputeLiveness(rec, base.Add(10*time.Second), time.Minute); got != LivenessReachable {
		t.Fatalf("got %q, want LivenessReachable", got)
	}
}

func TestComputeLivenessZeroTimeoutFailsClosed(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rec := DeviceRecord{NodeID: "n1", LastSeen: base}
	if got := ComputeLiveness(rec, base, 0); got != LivenessUnknown {
		t.Fatalf("got %q, want LivenessUnknown for zero timeout", got)
	}
}

func TestComputeLivenessClockSkewFailsClosed(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rec := DeviceRecord{NodeID: "n1", LastSeen: base}
	// "now" before LastSeen (clock skew, or a corrupted record): negative
	// age must never be treated as "very fresh".
	if got := ComputeLiveness(rec, base.Add(-time.Second), time.Minute); got != LivenessUnknown {
		t.Fatalf("got %q, want LivenessUnknown on negative age", got)
	}
}

func TestGetLivenessUnknownNode(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Now())
	store := NewRecordStore(newMemRecordBackend(), clock)
	liveness, err := GetLiveness(store, clock, "ghost", time.Minute)
	if err == nil {
		t.Fatal("expected error for unenrolled node")
	}
	if liveness != LivenessUnknown {
		t.Fatalf("got %q, want LivenessUnknown alongside the error", liveness)
	}
}

func TestGetLivenessEnrolledNode(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := NewRecordStore(newMemRecordBackend(), clock)
	id := testIdentity(t, "a")
	rec, err := store.Enroll(id, TierWorkerTrusted)
	if err != nil {
		t.Fatal(err)
	}
	rec.LastSeen = clock.Now()
	if err := store.put(rec); err != nil {
		t.Fatal(err)
	}
	liveness, err := GetLiveness(store, clock, id.NodeID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if liveness != LivenessReachable {
		t.Fatalf("got %q, want LivenessReachable", liveness)
	}
}

// TestLivenessThreeStateValuesAreDistinct proves R-21.225's three-state
// enum is exactly three distinct values, including LivenessUnavailable —
// the explicit-negative-signal state this ticket defines but does not
// itself produce (S-37.T2/S-37.T3's concern, per liveness.go's doc
// comment); nothing in this package's own heartbeat/doctor path ever
// returns it, but the value must exist and be distinguishable now so
// downstream consumers compile against the complete enum.
func TestLivenessThreeStateValuesAreDistinct(t *testing.T) {
	values := map[Liveness]bool{LivenessReachable: true, LivenessUnavailable: true, LivenessUnknown: true}
	if len(values) != 3 {
		t.Fatalf("expected 3 distinct Liveness values, got %d", len(values))
	}
	if LivenessUnavailable == LivenessReachable || LivenessUnavailable == LivenessUnknown {
		t.Fatal("LivenessUnavailable must be distinct from the other two states")
	}
}
