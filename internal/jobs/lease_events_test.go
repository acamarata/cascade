package jobs

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// newTestSink wires a leaseEventSink against REAL counterparts (Art.2):
// a real events.Bus, a real journal.SQLiteStore, and a real
// supervision.Store, all backed by the same in-memory provider.Store
// (storetest.NewMemStore) production wiring would share via one
// DomainSessions-namespaced KV -- never a package-local double of any
// of the three.
func newTestSink(t *testing.T) (*leaseEventSink, *events.Bus, journal.Store, *supervision.Store) {
	t.Helper()
	kv := storetest.NewMemStore()
	clock := runtime.NewFixedClock(time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC))
	bus := events.New(kv, clock)
	j := journal.New(kv, clock, journal.DefaultNamespace)
	var n atomic.Int64
	attn := supervision.NewStore(kv, clock, nil, func() string {
		n.Add(1)
		return "attn-id"
	}, 0)
	return newLeaseEventSink(bus, j, attn), bus, j, attn
}

// TestLeaseEventsAcquiredJournaledAndPublished proves the acquire path
// lands a real journal entry and a real bus event.
func TestLeaseEventsAcquiredJournaledAndPublished(t *testing.T) {
	sink, bus, j, _ := newTestSink(t)
	ctx := context.Background()
	lease := ResourceLease{RepoID: "repo-1", ScopeGlob: "internal/jobs/**", Holder: "job-a", Epoch: 1, State: LeaseHeld}
	if err := sink.acquired(ctx, lease); err != nil {
		t.Fatalf("acquired: %v", err)
	}

	entries, err := j.Replay(ctx, leaseEntityID(lease.RepoID, lease.ScopeGlob), journal.Cursor{}, nil)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(entries) != 1 || entries[0].Kind != journal.KindIntent || entries[0].OperationID != "acquired" {
		t.Fatalf("journal entries = %+v, want one KindIntent \"acquired\" entry", entries)
	}

	sub, err := bus.Subscribe(ctx, leaseEventNamespace, "test-cursor", 4)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	select {
	case ev := <-sub.Events:
		if ev.Kind != EventLeaseAcquired {
			t.Errorf("event kind = %v, want %v", ev.Kind, EventLeaseAcquired)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the acquired event (bounded receive)")
	}
}

// TestLeaseEventsExpiredPushesRealAttentionItem is the Art.2 integration
// proof step 6 requires: expiry pushes EXACTLY one item into the REAL
// R/S-39.T1 attention queue, idempotent on (kind, source_ref).
func TestLeaseEventsExpiredPushesRealAttentionItem(t *testing.T) {
	sink, _, _, attn := newTestSink(t)
	ctx := context.Background()
	lease := ResourceLease{RepoID: "repo-1", ScopeGlob: "internal/jobs/**", Holder: "job-a", Epoch: 1, State: LeaseExpiredUnconfirmed}

	if err := sink.expired(ctx, lease); err != nil {
		t.Fatalf("expired: %v", err)
	}
	if err := sink.expired(ctx, lease); err != nil { // idempotency: pushing the same (kind, source_ref) twice
		t.Fatalf("expired (second push): %v", err)
	}

	var found int
	items, err := attn.ListInScopes(ctx, []supervision.ScopeRef{{Kind: scope.ScopeKindProject, ID: lease.RepoID}}, supervision.Filter{IncludeAcked: true})
	if err != nil {
		t.Fatalf("ListInScopes: %v", err)
	}
	for _, it := range items {
		if it.SourceRef == leaseEntityID(lease.RepoID, lease.ScopeGlob) {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("attention items for this lease = %d, want exactly 1 (idempotent push)", found)
	}
}

// TestLeaseEventsFencedPushesAttentionItem covers step 10's attention
// integration on a fence mismatch.
func TestLeaseEventsFencedPushesAttentionItem(t *testing.T) {
	sink, _, _, attn := newTestSink(t)
	ctx := context.Background()
	if err := sink.fenced(ctx, "repo-1", "internal/jobs/**", 7); err != nil {
		t.Fatalf("fenced: %v", err)
	}
	items, err := attn.ListInScopes(ctx, []supervision.ScopeRef{{Kind: scope.ScopeKindProject, ID: "repo-1"}}, supervision.Filter{IncludeAcked: true})
	if err != nil {
		t.Fatalf("ListInScopes: %v", err)
	}
	if len(items) != 1 || items[0].SourceRef != leaseEntityID("repo-1", "internal/jobs/**") {
		t.Fatalf("attention items = %+v, want exactly one naming the fenced lease", items)
	}
}

// TestLeaseEventsNilSinkFieldsAreNoOps proves every sink method
// tolerates a partially-nil sink (only the DB mutation succeeded; no
// journal/bus/attention wiring configured), matching NewLeaseManager's
// documented "sink may be nil" contract at the LeaseManager level.
func TestLeaseEventsNilSinkFieldsAreNoOps(t *testing.T) {
	sink := newLeaseEventSink(nil, nil, nil)
	ctx := context.Background()
	lease := ResourceLease{RepoID: "repo-1", ScopeGlob: "internal/jobs/**", Holder: "job-a", Epoch: 1, State: LeaseHeld}
	if err := sink.acquired(ctx, lease); err != nil {
		t.Errorf("acquired with a nil-fields sink: %v", err)
	}
	if err := sink.expired(ctx, lease); err != nil {
		t.Errorf("expired with a nil-fields sink: %v", err)
	}
	if err := sink.fenced(ctx, "repo-1", "internal/jobs/**", 1); err != nil {
		t.Errorf("fenced with a nil-fields sink: %v", err)
	}
}
