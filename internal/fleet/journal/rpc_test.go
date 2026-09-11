// Purpose: RegisterHandlers/fleet.journal_show/replay's required tests:
//
//	happy path over a real *rpc.Registry, an unknown entity, an
//	empty-but-known journal, limit clamping, a cursor past the head, and
//	a malformed (negative) cursor. Uses a stub Reader (task 2's own
//	wording) rather than a real SQLiteStore — store.go/store_test.go
//	already cover real-store correctness; this file's subject is the RPC
//	wire layer above it. Client's own tests live in rpc_client_test.go
//	(split out purely to stay under the 300-line-per-file cap).
//
// SPORT: internal.fleet.journal.RegisterHandlers/ADDED (P1-E13-W3-S27-T4).
package journal_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// stubReader is a minimal in-memory journal.Reader double: a fixed head
// sequence per entity and a fixed entry slice per entity, no locking, no
// storage. entities absent from heads report head 0 (unknown).
type stubReader struct {
	heads   map[string]uint64
	entries map[string][]journal.Entry
	// headErr and replayErr, when set, are returned unconditionally by
	// HeadSeq/Replay instead of the map-driven happy-path behavior below
	// — used to prove resolveEntries propagates a reader failure rather
	// than swallowing it.
	headErr   error
	replayErr error
}

func (s *stubReader) HeadSeq(_ context.Context, entityID string) (uint64, error) {
	if s.headErr != nil {
		return 0, s.headErr
	}
	return s.heads[entityID], nil
}

func (s *stubReader) Replay(_ context.Context, entityID string, cursor journal.Cursor, _ []journal.Kind) ([]journal.Entry, error) {
	if s.replayErr != nil {
		return nil, s.replayErr
	}
	var out []journal.Entry
	for _, e := range s.entries[entityID] {
		if e.Seq > cursor.Seq {
			out = append(out, e)
		}
	}
	return out, nil
}

// makeEntries builds n synthetic entries for entityID, sequence 1..n.
func makeEntries(entityID string, n int) []journal.Entry {
	out := make([]journal.Entry, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, journal.Entry{
			EntityID: entityID, Seq: uint64(i), Kind: journal.KindIntent,
			OperationID: "op", TSUnixNano: int64(i),
		})
	}
	return out
}

func newStubRegistry(reader *stubReader) *rpc.Registry {
	registry := rpc.NewRegistry()
	journal.RegisterHandlers(registry, reader)
	return registry
}

func dispatchShow(t *testing.T, registry *rpc.Registry, params string) ([]journal.Entry, *rpc.ErrorObject) {
	t.Helper()
	req := &rpc.Request{JSONRPC: "2.0", Method: journal.MethodShow, Params: json.RawMessage(params)}
	result, errObj := registry.Dispatch(context.Background(), req)
	if errObj != nil {
		return nil, errObj
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var decoded struct {
		Entries []journal.Entry `json:"entries"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return decoded.Entries, nil
}

func dispatchReplay(t *testing.T, registry *rpc.Registry, params string) ([]journal.Entry, *rpc.ErrorObject) {
	t.Helper()
	req := &rpc.Request{JSONRPC: "2.0", Method: journal.MethodReplay, Params: json.RawMessage(params)}
	result, errObj := registry.Dispatch(context.Background(), req)
	if errObj != nil {
		return nil, errObj
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var decoded struct {
		Entries []journal.Entry `json:"entries"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return decoded.Entries, nil
}

// TestFleetJournalRPC_Show_HappyPath proves fleet.journal_show returns
// every seeded entry, in sequence order.
func TestFleetJournalRPC_Show_HappyPath(t *testing.T) {
	reader := &stubReader{heads: map[string]uint64{"e1": 3}, entries: map[string][]journal.Entry{"e1": makeEntries("e1", 3)}}
	registry := newStubRegistry(reader)

	entries, errObj := dispatchShow(t, registry, `{"entity_id":"e1"}`)
	if errObj != nil {
		t.Fatalf("Dispatch: %+v", errObj)
	}
	if len(entries) != 3 {
		t.Fatalf("fleet.journal_show = %d entries, want 3", len(entries))
	}
	for i, e := range entries {
		if e.Seq != uint64(i+1) {
			t.Errorf("entries[%d].Seq = %d, want %d (sequence order)", i, e.Seq, i+1)
		}
	}
}

// TestFleetJournalRPC_Show_AfterFiltersEntries proves --after (after_seq)
// filters entries at the cursor.
func TestFleetJournalRPC_Show_AfterFiltersEntries(t *testing.T) {
	reader := &stubReader{heads: map[string]uint64{"e1": 5}, entries: map[string][]journal.Entry{"e1": makeEntries("e1", 5)}}
	registry := newStubRegistry(reader)

	entries, errObj := dispatchShow(t, registry, `{"entity_id":"e1","after_seq":3}`)
	if errObj != nil {
		t.Fatalf("Dispatch: %+v", errObj)
	}
	if len(entries) != 2 || entries[0].Seq != 4 || entries[1].Seq != 5 {
		t.Fatalf("fleet.journal_show(after_seq=3) = %+v, want seq [4,5]", entries)
	}
}

// TestFleetJournalRPC_Show_EmptyButKnownJournal proves a known entity
// whose cursor is caught up to the head exits 0 with an empty list, not
// an error.
func TestFleetJournalRPC_Show_EmptyButKnownJournal(t *testing.T) {
	reader := &stubReader{heads: map[string]uint64{"e1": 3}, entries: map[string][]journal.Entry{"e1": makeEntries("e1", 3)}}
	registry := newStubRegistry(reader)

	entries, errObj := dispatchShow(t, registry, `{"entity_id":"e1","after_seq":3}`)
	if errObj != nil {
		t.Fatalf("Dispatch: %+v", errObj)
	}
	if len(entries) != 0 {
		t.Fatalf("fleet.journal_show(after_seq=head) = %d entries, want 0", len(entries))
	}
}

// TestFleetJournalUnknownEntity_Show proves an entity that has never had
// anything appended (head 0) returns a typed KindNotFound error, never an
// empty array.
func TestFleetJournalUnknownEntity_Show(t *testing.T) {
	reader := &stubReader{heads: map[string]uint64{}, entries: map[string][]journal.Entry{}}
	registry := newStubRegistry(reader)

	entries, errObj := dispatchShow(t, registry, `{"entity_id":"ghost"}`)
	if errObj == nil {
		t.Fatalf("Dispatch: want an error for an unknown entity, got %d entries", len(entries))
	}
	if errObj.Code != cascade.KindNotFound.JSONRPCCode() {
		t.Errorf("error code = %d, want KindNotFound's code %d", errObj.Code, cascade.KindNotFound.JSONRPCCode())
	}
}

// TestFleetJournalUnknownEntity_Replay proves the same refusal on the
// replay door.
func TestFleetJournalUnknownEntity_Replay(t *testing.T) {
	reader := &stubReader{heads: map[string]uint64{}, entries: map[string][]journal.Entry{}}
	registry := newStubRegistry(reader)

	_, errObj := dispatchReplay(t, registry, `{"entity_id":"ghost"}`)
	if errObj == nil {
		t.Fatal("Dispatch: want an error for an unknown entity")
	}
}

// TestFleetJournalRPC_LimitClamping proves a requested limit above
// maxShowLimit (500) is silently clamped, never returned as an error.
func TestFleetJournalRPC_LimitClamping(t *testing.T) {
	reader := &stubReader{heads: map[string]uint64{"e1": 600}, entries: map[string][]journal.Entry{"e1": makeEntries("e1", 600)}}
	registry := newStubRegistry(reader)

	entries, errObj := dispatchShow(t, registry, `{"entity_id":"e1","limit":1000}`)
	if errObj != nil {
		t.Fatalf("Dispatch: %+v", errObj)
	}
	if len(entries) != 500 {
		t.Fatalf("fleet.journal_show(limit=1000) = %d entries, want 500 (server clamp)", len(entries))
	}
}

// TestFleetJournalRPC_FutureAfterSeq proves a cursor past the entity's
// head is a typed error, not silently treated as "no entries".
func TestFleetJournalRPC_FutureAfterSeq(t *testing.T) {
	reader := &stubReader{heads: map[string]uint64{"e1": 3}, entries: map[string][]journal.Entry{"e1": makeEntries("e1", 3)}}
	registry := newStubRegistry(reader)

	_, errObj := dispatchShow(t, registry, `{"entity_id":"e1","after_seq":99}`)
	if errObj == nil {
		t.Fatal("Dispatch: want an error for after_seq beyond the entity's head")
	}
}

// TestFleetJournalRPC_NegativeAfterSeq proves a negative after_seq (an
// impossible value for the uint64 wire field) is a typed decode error,
// never a panic or a silently-accepted call.
func TestFleetJournalRPC_NegativeAfterSeq(t *testing.T) {
	reader := &stubReader{heads: map[string]uint64{"e1": 3}, entries: map[string][]journal.Entry{"e1": makeEntries("e1", 3)}}
	registry := newStubRegistry(reader)

	_, errObj := dispatchShow(t, registry, `{"entity_id":"e1","after_seq":-1}`)
	if errObj == nil {
		t.Fatal("Dispatch: want a decode error for a negative after_seq")
	}
}

// TestFleetJournalRPC_UnknownRequestField proves an unrecognized params
// key is refused, matching sessions.decodeListParams's convention.
func TestFleetJournalRPC_UnknownRequestField(t *testing.T) {
	reader := &stubReader{heads: map[string]uint64{"e1": 3}, entries: map[string][]journal.Entry{"e1": makeEntries("e1", 3)}}
	registry := newStubRegistry(reader)

	_, errObj := dispatchShow(t, registry, `{"entity_id":"e1","bogus_field":true}`)
	if errObj == nil {
		t.Fatal("Dispatch: want a decode error for an unrecognized params field")
	}
}

// TestFleetJournalRPC_Replay_MatchesShow proves replay re-emits the same
// entries, in the same sequence order, as show.
func TestFleetJournalRPC_Replay_MatchesShow(t *testing.T) {
	reader := &stubReader{heads: map[string]uint64{"e1": 4}, entries: map[string][]journal.Entry{"e1": makeEntries("e1", 4)}}
	registry := newStubRegistry(reader)

	shown, errObj := dispatchShow(t, registry, `{"entity_id":"e1"}`)
	if errObj != nil {
		t.Fatalf("show Dispatch: %+v", errObj)
	}
	replayed, errObj := dispatchReplay(t, registry, `{"entity_id":"e1"}`)
	if errObj != nil {
		t.Fatalf("replay Dispatch: %+v", errObj)
	}
	if len(shown) != len(replayed) {
		t.Fatalf("replay returned %d entries, show returned %d", len(replayed), len(shown))
	}
	for i := range shown {
		if shown[i].Seq != replayed[i].Seq {
			t.Errorf("entries[%d]: show seq %d != replay seq %d", i, shown[i].Seq, replayed[i].Seq)
		}
	}
}

// TestFleetJournalRPC_ReplayFromFiltersEntries proves --from (from_seq)
// filters entries the same way --after does for show.
func TestFleetJournalRPC_ReplayFromFiltersEntries(t *testing.T) {
	reader := &stubReader{heads: map[string]uint64{"e1": 5}, entries: map[string][]journal.Entry{"e1": makeEntries("e1", 5)}}
	registry := newStubRegistry(reader)

	entries, errObj := dispatchReplay(t, registry, `{"entity_id":"e1","from_seq":3}`)
	if errObj != nil {
		t.Fatalf("Dispatch: %+v", errObj)
	}
	if len(entries) != 2 || entries[0].Seq != 4 {
		t.Fatalf("fleet.journal_replay(from_seq=3) = %+v, want seq [4,5]", entries)
	}
}
