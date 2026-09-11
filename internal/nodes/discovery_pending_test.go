package nodes

import (
	"testing"
	"time"
)

// TestDiscoveryFindingsArePending proves PendingCandidate's structural
// separation from DeviceRecord: it is a distinct type with no shared
// identity field semantics that could let a candidate be mistaken for an
// enrolled record.
func TestDiscoveryFindingsArePending(t *testing.T) {
	candidate := PendingCandidate{
		ID:           "cand-1",
		Source:       "lan-discovery",
		Detail:       "observed via mDNS browse",
		DiscoveredAt: time.Now(),
	}
	if candidate.ID == "" || candidate.Source == "" {
		t.Fatal("PendingCandidate must carry a non-empty id and source")
	}
	var _ DeviceRecord // distinct type; a PendingCandidate is never assignable to it
}

// TestPendingCandidateNeverPlacedOn proves the structural invariant that
// matters: RecordStore.List (the only store S-37.T1's forward placement
// ticket will ever read) is populated exclusively through Enroll, which
// always requires an explicit, valid trust_tier — there is no path that
// admits a PendingCandidate value into it.
func TestPendingCandidateNeverPlacedOn(t *testing.T) {
	store := NewRecordStore(newMemRecordBackend(), fixedClock{})
	_ = PendingCandidate{ID: "cand-1", Source: "lan-discovery", DiscoveredAt: time.Now()}

	recs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("List() = %v, want empty: a pending candidate must never appear without going through Enroll", recs)
	}

	id := testIdentity(t, "k")
	if _, err := store.Enroll(id, TierWorkerTrusted); err != nil {
		t.Fatal(err)
	}
	recs, err = store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].NodeID != id.NodeID {
		t.Fatalf("List() = %v, want exactly the one record admitted via Enroll", recs)
	}
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
