package conversation

// Purpose: chat.append_turn's thread-target rules (adapter_appendtarget.go)
//   and the one-transaction first-turn write (Store.AppendFirstTurn). The
//   laundering probes NEW1/NEW2 from the confirm-4 review are here: neither
//   an unmarked-looking thread nor a caller-chosen id can be given a wider
//   marker through append_turn.
// SPORT: internal.conversation.adapter/CHANGED (tests) (P1-E22-W5-S47-T2).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestAppendTurnRefusesPrivacyModeOnCreatedThread is probe NEW1: a thread
// created restricted, then an append naming it with privacy_mode public
// and a leading block, is refused; the thread stays restricted with zero
// turns. Failing input: treating a zero-turn named thread as "creating".
func TestAppendTurnRefusesPrivacyModeOnCreatedThread(t *testing.T) {
	_, registry, store := newTestAdapterWithStore(t)
	ctx := context.Background()
	created, errObj := dispatch(t, registry, MethodCreateThread, createThreadParams{PrivacyMode: "restricted"})
	if errObj != nil {
		t.Fatalf("create_thread: %+v", errObj)
	}
	id := created.(createThreadResult).ThreadID
	_, errObj = dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: id, Role: "user", PrivacyMode: "public", LeadingContext: "SECRET-restricted",
		Segments: []appendSegmentWire{{Kind: "text", Content: "p"}},
	})
	if errObj == nil || errObj.Code != cascade.RPCCodeInvalidInput {
		t.Fatalf("append_turn{%s, privacy_mode: public} = %+v, want RPCCodeInvalidInput", id, errObj)
	}
	if got, _ := store.ThreadPrivacy(ctx, id); got != provider.SensitivityRestricted {
		t.Fatalf("thread tier = %s, want restricted (unchanged)", got)
	}
	if turns, _ := store.ListTurns(ctx, id); len(turns) != 0 {
		t.Fatalf("thread holds %d turns, want 0", len(turns))
	}
}

// TestAppendTurnRefusesCallerChosenThreadID is probe NEW2: a caller-chosen
// id that names no thread is refused -- with or without a privacy_mode --
// and nothing is created for it. Failing input: letting AppendTurn create
// the thread row on first use for a caller-named id.
func TestAppendTurnRefusesCallerChosenThreadID(t *testing.T) {
	_, registry, store := newTestAdapterWithStore(t)
	ctx := context.Background()
	const id = "attacker-chosen-id"
	for name, mode := range map[string]string{"with public mode": "public", "with no mode": ""} {
		_, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
			ThreadID: id, Role: "user", PrivacyMode: mode, LeadingContext: "SECRET-restricted",
			Segments: []appendSegmentWire{{Kind: "text", Content: "p"}},
		})
		if errObj == nil {
			t.Fatalf("%s: append_turn to a caller-chosen id was accepted", name)
		}
	}
	_, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: id, Role: "user", Segments: []appendSegmentWire{{Kind: "text", Content: "p"}},
	})
	if errObj == nil || errObj.Code != cascade.RPCCodeNotFound {
		t.Fatalf("append_turn to a nonexistent named thread = %+v, want RPCCodeNotFound", errObj)
	}
	if _, ok, _ := store.GetThread(ctx, id); ok {
		t.Fatal("a refused append created the caller-chosen thread")
	}
	if n := markerRows(t, store, id); n != 0 {
		t.Fatalf("marker rows for %s = %d, want 0", id, n)
	}
}

// TestAppendTurnRefusedLeavesNoMarker is the bogus-segment-kind probe: an
// append that would CREATE a thread with privacy_mode public but carries a
// segment kind the store refuses leaves no thread, no turn and no marker.
// Failing input: writing the marker before the segments are decoded.
func TestAppendTurnRefusedLeavesNoMarker(t *testing.T) {
	_, registry, store := newTestAdapterWithStore(t)
	ctx := context.Background()
	_, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		Role: "user", PrivacyMode: "public",
		Segments: []appendSegmentWire{{Kind: "bogus-kind", Content: "p"}},
	})
	if errObj == nil {
		t.Fatal("append_turn with a bogus segment kind was accepted")
	}
	threads, err := store.ListThreads(ctx)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 0 {
		t.Fatalf("a refused append left %d threads behind", len(threads))
	}
	var n int
	db := store.(*conversationStore).db
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+tableThreadPrivacy).Scan(&n); err != nil {
		t.Fatalf("count markers: %v", err)
	}
	if n != 0 {
		t.Fatalf("a refused append left %d marker rows behind, want 0", n)
	}
}

// TestAppendFirstTurnRollsBackMarker proves the marker and the first turn
// are ONE transaction: a segment the store refuses inside the transaction
// (a duplicate segment id) rolls back the thread, the turn and the marker.
// Failing input: committing the marker in its own transaction first.
func TestAppendFirstTurnRollsBackMarker(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	turn := Turn{ID: NewTurnID("th-tx", 0, RoleUser), ThreadID: "th-tx", Seq: 0, Role: RoleUser, CreatedAt: 1}
	segID := NewSegmentID(turn.ID, 0, SegmentText)
	segs := []Segment{
		{ID: segID, TurnID: turn.ID, Seq: 0, Kind: SegmentText, Content: "a", CreatedAt: 1},
		{ID: segID, TurnID: turn.ID, Seq: 1, Kind: SegmentText, Content: "b", CreatedAt: 1},
	}
	if err := store.AppendFirstTurn(ctx, provider.SensitivityPublic, turn, segs); err == nil {
		t.Fatal("AppendFirstTurn with a duplicate segment id succeeded")
	}
	if _, ok, _ := store.GetThread(ctx, "th-tx"); ok {
		t.Fatal("the thread row survived the rolled-back first turn")
	}
	if n := markerRows(t, store, "th-tx"); n != 0 {
		t.Fatalf("marker rows = %d after the rollback, want 0", n)
	}
	if err := store.AppendFirstTurn(ctx, provider.SensitivityPublic, turn, segs[:1]); err != nil {
		t.Fatalf("AppendFirstTurn (valid): %v", err)
	}
	if got, _ := store.ThreadPrivacy(ctx, "th-tx"); got != provider.SensitivityPublic {
		t.Fatalf("ThreadPrivacy = %s, want public", got)
	}
}

// TestAppendFirstTurn_RefusesMalformedInput covers validateFirstTurn's
// refusals: an invalid tier, a non-zero Seq, a segment of another turn and
// a gap in segment numbering, each before any write.
func TestAppendFirstTurn_RefusesMalformedInput(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	turn := Turn{ID: NewTurnID("th-bad", 0, RoleUser), ThreadID: "th-bad", Seq: 0, Role: RoleUser, CreatedAt: 1}
	seg := Segment{ID: NewSegmentID(turn.ID, 0, SegmentText), TurnID: turn.ID, Seq: 0, Kind: SegmentText, Content: "a", CreatedAt: 1}
	late := turn
	late.Seq = 1
	foreign := seg
	foreign.TurnID = "other-turn"
	gap := seg
	gap.Seq = 2
	cases := map[string]func() error{
		"invalid tier":   func() error { return store.AppendFirstTurn(ctx, provider.SensitivityTier(99), turn, nil) },
		"non-zero seq":   func() error { return store.AppendFirstTurn(ctx, provider.SensitivityPublic, late, nil) },
		"foreign turn":   func() error { return store.AppendFirstTurn(ctx, provider.SensitivityPublic, turn, []Segment{foreign}) },
		"numbering gap":  func() error { return store.AppendFirstTurn(ctx, provider.SensitivityPublic, turn, []Segment{gap}) },
		"invalid record": func() error { return store.AppendFirstTurn(ctx, provider.SensitivityPublic, Turn{}, nil) },
	}
	for name, call := range cases {
		if err := call(); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("%s: err = %v, want KindInvalidInput", name, err)
		}
	}
	if n := markerRows(t, store, "th-bad"); n != 0 {
		t.Fatalf("marker rows = %d, want 0", n)
	}
}

// TestAppendTurnCreatingWithJournal proves a thread-creating append with a
// JournalStore wired still commits thread, marker and turn together, and
// journals the intent and the ack around that one transaction.
func TestAppendTurnCreatingWithJournal(t *testing.T) {
	store := newTestStore(t)
	adapter := NewAdapter(store, &fakeBus{}, passthroughSubst{}, newAdapterTestClock(), "")
	js := newTestJournal(t)
	adapter.SetJournal(js)
	registry := rpc.NewRegistry()
	adapter.RegisterHandlers(registry)
	res, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		Role: "user", PrivacyMode: "local-only", Segments: []appendSegmentWire{{Kind: "text", Content: "journaled"}},
	})
	if errObj != nil {
		t.Fatalf("append_turn: %+v", errObj)
	}
	id := res.(appendTurnResult).ThreadID
	if got, _ := store.ThreadPrivacy(context.Background(), id); got != provider.SensitivityLocalOnly {
		t.Fatalf("ThreadPrivacy = %s, want local-only", got)
	}
	for _, kind := range []journal.Kind{journal.KindIntent, journal.KindAck} {
		entries, err := js.Replay(context.Background(), id, journal.Cursor{}, []journal.Kind{kind})
		if err != nil || len(entries) != 1 {
			t.Fatalf("journal %v entries = %d (%v), want 1", kind, len(entries), err)
		}
	}
}

// seedOwnedThread creates id as an existing thread at tier, attributed to the
// owner. Store.CreateThread writes the attribution row, so the scope schema
// is applied first, as chat.create_thread does before it mints a thread.
func seedOwnedThread(t testing.TB, store Store, id string, tier provider.SensitivityTier) {
	t.Helper()
	ctx := context.Background()
	if cs, ok := store.(*conversationStore); ok {
		if err := ensureScopeSchema(ctx, cs.db, newTestClock()); err != nil {
			t.Fatalf("scope schema: %v", err)
		}
	}
	if err := store.CreateThread(ctx, id, tier, ownerScope, 1); err != nil {
		t.Fatalf("seed thread %s: %v", id, err)
	}
}

// seedNamedThreads creates each id as an existing restricted thread. Tests
// written when chat.append_turn still created a caller-named thread on
// first use state that precondition explicitly now.
func seedNamedThreads(t testing.TB, store Store, ids ...string) {
	t.Helper()
	for _, id := range ids {
		seedOwnedThread(t, store, id, provider.SensitivityRestricted)
	}
}

// TestCreatingAppendWritesRestrictedMarker: a creating chat.append_turn
// (no thread_id) with an empty privacy_mode writes an EXPLICIT restricted
// marker in the same transaction as the thread and the turn, so the tier is
// stored rather than left to a reader's absent-row fallback. Failing input:
// skipping the marker for an empty mode (the pre-fix write path).
func TestCreatingAppendWritesRestrictedMarker(t *testing.T) {
	_, registry, store := newTestAdapterWithStore(t)
	res, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		Role: "user", Segments: []appendSegmentWire{{Kind: "text", Content: "first"}},
	})
	if errObj != nil {
		t.Fatalf("creating append: %+v", errObj)
	}
	id := res.(appendTurnResult).ThreadID
	if n := markerRows(t, store, id); n != 1 {
		t.Fatalf("marker rows for the created thread = %d, want exactly 1 (an explicit restricted marker)", n)
	}
	var stored string
	db := store.(*conversationStore).db
	if err := db.QueryRowContext(context.Background(),
		`SELECT privacy_mode FROM `+tableThreadPrivacy+` WHERE thread_id = ?`, id).Scan(&stored); err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if turns, _ := store.ListTurns(context.Background(), id); stored != provider.SensitivityRestricted.String() || len(turns) != 1 {
		t.Fatalf("stored marker %q with %d turns, want %q with 1 turn", stored, len(turns), provider.SensitivityRestricted.String())
	}
}
