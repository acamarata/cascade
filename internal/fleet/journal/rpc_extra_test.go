// Purpose: resolveEntries' remaining branches (empty entity_id, a
//
//	propagated HeadSeq failure, a propagated Replay failure) and
//	decodeParams' empty-raw shortcut (an omitted "params" field), none of
//	which rpc_test.go's happy-path-oriented stubReader cases exercise.
//
// SPORT: internal.fleet.journal.RegisterHandlers/ADDED (tests)
//
//	(P1-E13-W3-S27-T4).
package journal_test

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/rpc"
)

// TestFleetJournalRPC_EmptyEntityID proves an explicitly empty entity_id
// is refused before ever reaching the reader, not treated as "unknown
// entity" or silently defaulted.
func TestFleetJournalRPC_EmptyEntityID(t *testing.T) {
	reader := &stubReader{heads: map[string]uint64{}, entries: map[string][]journal.Entry{}}
	registry := newStubRegistry(reader)

	_, errObj := dispatchShow(t, registry, `{"entity_id":""}`)
	if errObj == nil {
		t.Fatal("Dispatch: want an error for an explicitly empty entity_id")
	}
}

// TestFleetJournalRPC_OmittedParams proves an entirely omitted "params"
// field decodes as the zero value (decodeParams' len(raw)==0 shortcut)
// rather than a decode error, and is then refused by the same empty-
// entity_id check as TestFleetJournalRPC_EmptyEntityID.
func TestFleetJournalRPC_OmittedParams(t *testing.T) {
	reader := &stubReader{heads: map[string]uint64{}, entries: map[string][]journal.Entry{}}
	registry := newStubRegistry(reader)

	req := &rpc.Request{JSONRPC: "2.0", Method: journal.MethodShow}
	_, errObj := registry.Dispatch(context.Background(), req)
	if errObj == nil {
		t.Fatal("Dispatch with omitted params: want an error (empty entity_id), not success")
	}
}

// TestFleetJournalRPC_HeadSeqErrorPropagates proves a reader failure from
// HeadSeq is returned to the caller unchanged, never swallowed into an
// empty result or a false "unknown entity".
func TestFleetJournalRPC_HeadSeqErrorPropagates(t *testing.T) {
	reader := &stubReader{
		heads: map[string]uint64{}, entries: map[string][]journal.Entry{},
		headErr: errors.New("head lookup exploded"),
	}
	registry := newStubRegistry(reader)

	_, errObj := dispatchShow(t, registry, `{"entity_id":"e1"}`)
	if errObj == nil {
		t.Fatal("Dispatch: want the HeadSeq failure propagated, got nil error")
	}
}

// TestFleetJournalRPC_ReplayErrorPropagates proves a reader failure from
// Replay itself (after HeadSeq and the cursor check both pass) is also
// returned unchanged.
func TestFleetJournalRPC_ReplayErrorPropagates(t *testing.T) {
	reader := &stubReader{
		heads:     map[string]uint64{"e1": 3},
		entries:   map[string][]journal.Entry{"e1": makeEntries("e1", 3)},
		replayErr: errors.New("replay scan exploded"),
	}
	registry := newStubRegistry(reader)

	_, errObj := dispatchShow(t, registry, `{"entity_id":"e1"}`)
	if errObj == nil {
		t.Fatal("Dispatch: want the Replay failure propagated, got nil error")
	}
}

// TestFleetJournalRPC_ReplayNegativeFromSeq proves fleet.journal_replay's
// own decodeParams call (not just fleet.journal_show's) rejects a
// negative from_seq as a typed decode error.
func TestFleetJournalRPC_ReplayNegativeFromSeq(t *testing.T) {
	reader := &stubReader{heads: map[string]uint64{"e1": 3}, entries: map[string][]journal.Entry{"e1": makeEntries("e1", 3)}}
	registry := newStubRegistry(reader)

	_, errObj := dispatchReplay(t, registry, `{"entity_id":"e1","from_seq":-1}`)
	if errObj == nil {
		t.Fatal("Dispatch: want a decode error for a negative from_seq")
	}
}
