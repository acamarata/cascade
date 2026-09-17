package daemon

// Purpose: the three recovery collaborators S-37.T3 left nil, wired to the
//   things this daemon already has open (P1-E17-W4-S37-T6). Split into its
//   own file because node_dispatch_rpc.go is at 245 lines and these are
//   adapters, not dispatch composition.
// Inputs: the record store and clock the dispatch composition already
//   holds; the journal store RegisterNodeDispatchJournal mounts; the
//   attention store RegisterFleetAttentionHandler serves.
// Outputs: a tunnel-state lookup, a JournalContinuityReader and an
//   AttentionFiler — the three whose absence made PlanRequeue refuse.
// SPORT: internal/daemon status:requeue-collaborators (P1-E17-W4-S37-T6).

import (
	"context"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// errNoJournalStore refuses a continuity read on a daemon with no journal
// store open. A refusal, not an empty slice: "I could not read the
// journal" and "there is nothing in the journal" produce the same resume
// point and mean opposite things.
func errNoJournalStore() error {
	return cascade.New(cascade.KindUnavailable,
		"daemon: no journal store is open, so nothing here can say what the lost attempt recorded")
}

// errNoAttentionStore refuses a hold nobody would be told about.
func errNoAttentionStore() error {
	return cascade.New(cascade.KindUnavailable,
		"daemon: no attention queue is open, so a held dispatch would reach nobody")
}

// dispatchTunnels answers placement's tunnel-state question from the
// heartbeat, which in this architecture is the only honest source there is.
//
// THE DECISION (R-14.274). Placement wants "is this node connected". The
// controller never dials a node: the node dials out and the session carries
// a REVERSE forward, so the controller holds no socket it could inspect and
// no per-node tunnel object — the S-36.T3 manager lives on the node side.
// What the controller does hold is the heartbeat, and the heartbeat travels
// over that same tunnel. Its arrival within the timeout is therefore direct
// evidence the tunnel was up; its absence is the only evidence of the
// opposite that exists here.
//
// The mapping FAILS CLOSED at the one place it could go either way:
// LivenessUnknown becomes TunnelDown, not TunnelReconnecting, because
// "reconnecting" would tell placement a node is on its way back when what
// this daemon actually knows is nothing at all.
func dispatchTunnels(records *nodes.RecordStore, clock runtime.Clock) nodes.TunnelStateLookup {
	if records == nil || clock == nil {
		return nil
	}
	return func(nodeID string) nodes.TunnelState {
		rec, err := records.Get(nodeID)
		if err != nil {
			return nodes.TunnelDown
		}
		if nodes.ComputeLiveness(rec, clock.Now(), nodes.DefaultHeartbeatTimeout) == nodes.LivenessReachable {
			return nodes.TunnelUp
		}
		return nodes.TunnelDown
	}
}

// journalContinuity reads what a lost attempt already recorded, out of the
// SAME journal store the dispatch stream appends to.
//
// One store, not two: a continuity reader over a second connection to the
// same database would still be reading a different store's idea of what has
// been checkpointed, and the resume point it produced would be a guess
// dressed as a fact.
type journalContinuity struct{ store *journal.SQLiteStore }

// RecordsSinceCheckpoint returns the node-streamed records after the
// entity's last published checkpoint, oldest first.
//
// Only KindNodeStream entries are read. An entity's log carries other
// kinds, and a resume point built from an entry the node did not stream
// would tell a replacement it had already done work that never reached it.
func (c journalContinuity) RecordsSinceCheckpoint(ctx context.Context, entityID string) ([]nodes.StreamedRecord, error) {
	if c.store == nil {
		return nil, errNoJournalStore()
	}
	entries, err := c.store.Replay(ctx, entityID, journal.Cursor{}, []journal.Kind{journal.KindNodeStream})
	if err != nil {
		return nil, err
	}
	out := make([]nodes.StreamedRecord, 0, len(entries))
	for _, e := range entries {
		// A record whose stamp will not decode is an ERROR, not a skip.
		// Skipping it would shorten the resume point silently, and a
		// replacement would re-run the work that entry accounted for.
		entry, decodeErr := nodes.DecodeStreamedEntry(e.Payload)
		if decodeErr != nil {
			return nil, decodeErr
		}
		out = append(out, nodes.StreamedRecord{
			Seq: e.Seq, Attempt: entry.Attempt, OperationID: e.OperationID,
		})
	}
	return out, nil
}

// attentionFiler puts a held outcome in the queue `cascade fleet attention`
// reads.
type attentionFiler struct{ store *supervision.Store }

// FileUnknownOutcome pushes the hold as an attention item.
//
// KindError, not KindStall: a stall is work that has stopped moving and
// will move again once something unblocks. This is an action that may
// already have run exactly once, may have run zero times, and must not be
// run again until a person says which — the queue's own vocabulary has no
// closer member, and inventing a fifth would reopen a closed enumeration
// for one caller.
//
// SourceRef is the DISPATCH id, because that is what the operator has in
// front of them and what the node's dedup log keys on. Priority 0 sorts it
// to the top of the default view: work nobody can safely repeat is the
// most urgent thing in the queue.
func (f attentionFiler) FileUnknownOutcome(ctx context.Context, held nodes.HeldOutcome) error {
	if f.store == nil {
		return errNoAttentionStore()
	}
	_, err := f.store.Push(ctx, supervision.AttentionItem{
		Kind:      supervision.KindError,
		SourceRef: held.DispatchID,
		ScopeRef:  scope.Ref{Kind: scope.ScopeKindGlobal, ID: "fleet"},
		Priority:  0,
	})
	return err
}

// NewAttentionStore builds the ONE attention store this daemon has.
//
// Shared by RegisterFleetAttentionHandler (which serves it over RPC) and
// by the dispatch recovery path (which writes to it). Two stores over one
// namespace would be two views of one queue, and a held dispatch filed
// into one of them would be invisible in the other.
func NewAttentionStore(store provider.Store, clock runtime.Clock, bus *events.Bus) *supervision.Store {
	if store == nil {
		return nil
	}
	var eventBus supervision.EventBus
	if bus != nil {
		eventBus = bus
	}
	return supervision.NewStore(store, clock, eventBus, supervision.NewSystemIDGenerator(), 0)
}
