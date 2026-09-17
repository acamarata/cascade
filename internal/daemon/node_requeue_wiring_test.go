package daemon

// Purpose (this file): the three recovery collaborators, each proved
//   against the REAL thing it adapts — a real journal store the dispatch
//   stream writes to, the real attention queue `fleet.attention.list`
//   serves, and the real liveness the record store derives.
// WHY THAT MATTERS HERE: every one of these was nil before, and
//   nodes.PlanRequeue refuses on a nil one. A test that only proved they
//   are non-nil would pass for an adapter that returns nothing.
// SPORT: internal/daemon status:requeue-collaborators (ADD tests) —
//   P1-E17-W4-S37-T6.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestContinuityReadsWhatTheDispatchStreamWrote is the end-to-end pairing
// that matters: the record goes in through the SAME verb a node streams
// through, and comes back out through the reader recovery uses. A reader
// tested against a hand-built journal entry would prove nothing about the
// shape the dispatch stream actually writes.
func TestContinuityReadsWhatTheDispatchStreamWrote(t *testing.T) {
	clock := runtime.NewSystemClock()
	store := journal.New(storetest.NewMemStore(), clock, "nodes.dispatch")
	registry := rpc.NewRegistry()
	dispatcher, _ := RegisterNodeDispatchHandlers(registry,
		nodes.NewRecordStore(nodes.NewFileRecordBackend(t.TempDir()), clock), clock,
		func() (nodes.Section, error) { return configuredSection(), nil },
		RecoveryStores{Journal: store},
	)
	RegisterNodeDispatchJournal(registry, dispatcher, store)

	// The attempt has to be the CURRENT one or the stream verb fences it
	// out, which is the rule that keeps a superseded attempt from leaving
	// a partial account of itself behind.
	attempt := dispatcher.Attempts().Next("d-1")
	streamRecord(t, registry, attempt)

	records, err := (journalContinuity{store: store}).RecordsSinceCheckpoint(context.Background(), "job-1")
	if err != nil {
		t.Fatalf("reading back what the stream verb wrote: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("read %d records, want the one the stream verb appended", len(records))
	}
	if records[0].Attempt != attempt {
		t.Errorf("attempt = %d, want %d; a resume point built on attempt zero would be fenced out",
			records[0].Attempt, attempt)
	}
	if records[0].OperationID != "op-7" {
		t.Errorf("operation id = %q, want op-7", records[0].OperationID)
	}
	if records[0].Seq == 0 {
		t.Error("sequence = 0; a replacement would resume from the start of the entity")
	}
}

// streamRecord sends one record through the real node.dispatch.journal verb.
func streamRecord(t *testing.T, registry *rpc.Registry, attempt uint64) {
	t.Helper()
	params, err := json.Marshal(nodes.JournalRecord{
		DispatchID: "d-1", Attempt: attempt, EntityID: "job-1",
		OperationID: "op-7", Payload: json.RawMessage(`{"note":"work"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, errObj := registry.Dispatch(context.Background(),
		&rpc.Request{Method: nodes.DispatchJournalMethod, Params: params}); errObj != nil {
		t.Fatalf("streaming a journal record: %+v", errObj)
	}
}

// TestContinuityWithNoStoreRefuses pins the rule that "I could not read
// the journal" is never answered as "there is nothing in it" — the two
// produce the same resume point and mean opposite things.
func TestContinuityWithNoStoreRefuses(t *testing.T) {
	_, err := (journalContinuity{}).RecordsSinceCheckpoint(context.Background(), "job-1")
	if err == nil {
		t.Fatal("a continuity read with no journal store reported an empty history")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Errorf("err kind = %v (ok=%v), want KindUnavailable", kind, ok)
	}
}

// TestAHeldOutcomeReachesTheQueueAPersonReads is the acceptance: filed
// through the dispatch path's filer, read back through the RPC the
// operator's own verb calls — not through a second store over the same
// namespace.
func TestAHeldOutcomeReachesTheQueueAPersonReads(t *testing.T) {
	kv := storetest.NewMemStore()
	clock := runtime.NewSystemClock()
	queue := NewAttentionStore(kv, clock, nil)

	err := (attentionFiler{store: queue}).FileUnknownOutcome(context.Background(), nodes.HeldOutcome{
		DispatchID: "d-held", ActionID: "a-1", NodeID: "lost", Attempt: 2,
		Signal: nodes.LossTunnel, Reason: "the tunnel dropped after the action was shipped",
	})
	if err != nil {
		t.Fatalf("filing a held outcome: %v", err)
	}

	// Listed through the SCOPE the filer files under, which is the same
	// resolution the RPC the operator's verb calls goes through: an item
	// filed into a scope nobody lists is an item nobody reads.
	items, listErr := queue.ListInScopes(context.Background(),
		[]supervision.ScopeRef{{Kind: scope.ScopeKindGlobal, ID: "fleet"}}, supervision.Filter{})
	if listErr != nil {
		t.Fatalf("listing the queue: %v", listErr)
	}
	if len(items) != 1 {
		t.Fatalf("the queue holds %d items, want the held dispatch", len(items))
	}
	if items[0].SourceRef != "d-held" {
		t.Errorf("source ref = %q, want the dispatch id an operator has in front of them", items[0].SourceRef)
	}
	if items[0].Kind != supervision.KindError {
		t.Errorf("kind = %q, want %q", items[0].Kind, supervision.KindError)
	}
	if items[0].Acked() {
		t.Error("a freshly filed hold is already acknowledged")
	}
}

// TestFilingWithNoQueueRefuses keeps a hold nobody was told about from
// reading as a hold somebody was.
func TestFilingWithNoQueueRefuses(t *testing.T) {
	err := (attentionFiler{}).FileUnknownOutcome(context.Background(), nodes.HeldOutcome{
		DispatchID: "d", ActionID: "a", NodeID: "n",
	})
	if err == nil {
		t.Fatal("a held outcome was accepted with no queue open")
	}
}

// TestTheTunnelReadingComesFromTheHeartbeat covers the decision R-14.274
// records: in a reverse-forward architecture the heartbeat is the only
// evidence of a live tunnel this side holds, and anything short of a
// recent one reads as DOWN rather than as reconnecting.
func TestTheTunnelReadingComesFromTheHeartbeat(t *testing.T) {
	clock := runtime.NewSystemClock()
	records := nodes.NewRecordStore(nodes.NewFileRecordBackend(t.TempDir()), clock)
	lookup := dispatchTunnels(records, clock)
	if lookup == nil {
		t.Fatal("no lookup was built; placement would place nothing")
	}
	// A node this store has never heard of is DOWN, not up.
	if got := lookup("never-enrolled"); got != nodes.TunnelDown {
		t.Errorf("an unknown node reads as %v, want %v", got, nodes.TunnelDown)
	}
	if got := dispatchTunnels(nil, clock); got != nil {
		t.Error("a lookup was built over a nil record store; it would answer for nodes it cannot see")
	}
}

// TestTheHeartbeatMappingIsFailClosed drives the mapping directly over the
// three liveness values, because the store-backed test above can only
// reach one of them.
func TestTheHeartbeatMappingIsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lastSeen time.Time
		want     nodes.TunnelState
	}{
		{"a heartbeat inside the timeout", time.Now(), nodes.TunnelUp},
		{"a heartbeat older than the timeout", time.Now().Add(-nodes.DefaultHeartbeatTimeout * 2), nodes.TunnelDown},
		{"a node that has never been seen", time.Time{}, nodes.TunnelDown},
	} {
		got := nodes.TunnelDown
		if nodes.ComputeLiveness(nodes.DeviceRecord{LastSeen: tc.lastSeen},
			time.Now(), nodes.DefaultHeartbeatTimeout) == nodes.LivenessReachable {
			got = nodes.TunnelUp
		}
		if got != tc.want {
			t.Errorf("%s reads as %v, want %v", tc.name, got, tc.want)
		}
	}
}
