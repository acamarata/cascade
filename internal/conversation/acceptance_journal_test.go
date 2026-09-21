package conversation

// Purpose: TestAcceptanceJournalResume -- S-44.T5 acceptance criterion 5.
//   Differs from journal_test.go's own TestConversationKillResume (S-44.T4,
//   already real and already green) in exactly one respect: T4's test
//   drives AppendTurnJournaled and Resume as bare package functions; this
//   acceptance test drives the same kill/restart/resume story through
//   Adapter.SetJournal + chat.append_turn dispatched over a real
//   *rpc.Registry, proving SetJournal's own wiring inside handleAppendTurn
//   (the `if a.journal != nil` branch T4 added), and reads the recovered
//   state back through chat.get_thread -- the real RPC poll path --
//   rather than a direct Store call.
// HONEST GAP (verified by grep, not disclosed by T4's own journal): this
//   test does NOT prove "a running daemon survives a crash." `grep -rln
//   SetJournal --include='*.go' .` (excluding _test.go) hits only
//   internal/conversation/adapter.go (SetJournal's own definition) and a
//   comment in scrub.go -- cmd/cascade/chat_wiring.go's wireChatHandlers,
//   the daemon's one real composition root for the chat RPC surface,
//   never calls adapter.SetJournal(...). So today, a real `cascade`
//   daemon never wires a journal into its chat adapter at all, and a real
//   crash loses every conversation turn in flight; this is a broader gap
//   than T4's own journal discloses (T4 names daemon-startup Resume/
//   checkpoint-job scheduling as unwired, but not that SetJournal itself
//   has zero production callers). This test builds its OWN adapter by
//   hand (newAcceptanceJournaledAdapter, below) and calls SetJournal
//   itself -- it proves the journal mechanism and handleAppendTurn's
//   wiring of it are correct, and nothing more. It cannot and does not
//   catch "wireChatHandlers never calls SetJournal," because that is true
//   today and stays green under this test regardless. A follow-up ticket
//   to wire SetJournal into wireChatHandlers is required before this
//   acceptance criterion holds for a real daemon.
// Named failing inputs: Resume applied == 0 after a genuine pre-kill
//   commit (the turn is lost); a second Resume call returning a nonzero
//   applied count (a non-idempotent replay, i.e. duplication); a
//   chat.get_thread poll against the restarted daemon that omits the
//   recovered turn or returns it with different content.
// SPORT: internal.conversation/acceptance (ADDED, tests-only) (P1-E20-W5-S44-T5).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

const acceptanceJournalThread = "th-accept-journal-resume"

// newAcceptanceJournaledAdapter wires js onto a fresh, hand-built Adapter
// over store and returns the registry a test dispatches chat.* calls
// against -- SetJournal's own documented effect (adapter.go:82), exercised
// directly by this test, NOT through wireChatHandlers: see this file's
// header HONEST GAP note -- the real daemon composition root never calls
// SetJournal, so this hand-wiring is the only place that happens today.
// A real events.Bus is wired too (mode "", not ModeEmbedded): a nil bus
// in non-embedded mode makes emitTurnAppended refuse every append
// (sse.go), and this test's concern -- journal wiring surviving a
// restart -- should exercise the same non-embedded append path a real
// daemon runs, not the Windows tier-2 SSE-less one.
func newAcceptanceJournaledAdapter(store Store, js JournalStore) *rpc.Registry {
	bus := events.New(storetest.NewMemStore(), newTestClock())
	adapter := NewAdapter(store, bus, passthroughSubst{}, newAdapterTestClock(), "")
	adapter.SetJournal(js)
	registry := rpc.NewRegistry()
	adapter.RegisterHandlers(registry)
	return registry
}

// acceptanceJournalPreKillCommit issues the two real chat.append_turn calls
// through the wired RPC path -- not AppendTurnJournaled called directly --
// so a regression that removes Adapter.SetJournal's wiring (adapter.go's
// `if a.journal != nil` branch) fails THIS test, not only journal_test.go's
// -- then confirms both landed via chat.get_thread before any kill/restart.
// Split out of TestAcceptanceJournalResume to keep it under Art.10.3's
// 50-line cap; returns the two committed turn ids.
func acceptanceJournalPreKillCommit(t *testing.T, registry *rpc.Registry) (firstTurnID, secondTurnID string) {
	t.Helper()
	first, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: acceptanceJournalThread, Role: "user",
		Segments: []appendSegmentWire{{Kind: "text", Content: "pre-kill turn one"}},
	})
	if errObj != nil {
		t.Fatalf("pre-kill append_turn #1: %+v", errObj)
	}
	second, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
		ThreadID: acceptanceJournalThread, Role: "user",
		Segments: []appendSegmentWire{{Kind: "text", Content: "pre-kill turn two"}},
	})
	if errObj != nil {
		t.Fatalf("pre-kill append_turn #2: %+v", errObj)
	}
	preKillThread, errObj := dispatch(t, registry, MethodGetThread, getThreadParams{ThreadID: acceptanceJournalThread})
	if errObj != nil {
		t.Fatalf("pre-kill get_thread: %+v", errObj)
	}
	if n := len(preKillThread.(getThreadResult).Turns); n != 2 {
		t.Fatalf("pre-kill get_thread returned %d turns, want 2 -- test setup is wrong before any kill/restart", n)
	}
	return first.(appendTurnResult).TurnID, second.(appendTurnResult).TurnID
}

// acceptanceJournalAssertRecovered reads the recovered state back through
// the REAL RPC poll path (chat.get_thread on a NEW Adapter/registry over
// the restarted store), not a direct Store.ListTurns call -- this is what
// "the last committed turn is present" must mean to an actual client.
func acceptanceJournalAssertRecovered(t *testing.T, registry *rpc.Registry, firstTurnID, secondTurnID string) {
	t.Helper()
	restartedThread, errObj := dispatch(t, registry, MethodGetThread, getThreadParams{ThreadID: acceptanceJournalThread})
	if errObj != nil {
		t.Fatalf("post-restart get_thread: %+v", errObj)
	}
	restartedResult := restartedThread.(getThreadResult)
	if len(restartedResult.Turns) != 2 {
		t.Fatalf("post-restart get_thread returned %d turns, want 2 -- a turn was lost across restart", len(restartedResult.Turns))
	}
	byID := make(map[string]turnWithSegs, 2)
	for _, tw := range restartedResult.Turns {
		byID[tw.Turn.ID] = tw
	}
	for id, wantContent := range map[string]string{firstTurnID: "pre-kill turn one", secondTurnID: "pre-kill turn two"} {
		tw, ok := byID[id]
		if !ok {
			t.Fatalf("post-restart get_thread is missing turn %s", id)
		}
		if len(tw.Segments) != 1 || tw.Segments[0].Content != wantContent {
			t.Fatalf("post-restart turn %s segments = %+v, want exactly one segment with content %q", id, tw.Segments, wantContent)
		}
	}
}

func TestAcceptanceJournalResume(t *testing.T) {
	js := newTestJournal(t)
	preKillStore := newTestStore(t)
	preKillRegistry := newAcceptanceJournaledAdapter(preKillStore, js)
	firstTurnID, secondTurnID := acceptanceJournalPreKillCommit(t, preKillRegistry)

	// "kill -9": the process and its data store are gone; only js (a
	// separate durable log, per journal.go's own documented model)
	// survives. A brand new, empty store simulates the rebuild.
	restartedStore := newTestStore(t)
	applied, err := Resume(context.Background(), js, restartedStore, acceptanceJournalThread)
	if err != nil {
		t.Fatalf("Resume after simulated kill -9: %v", err)
	}
	if applied == 0 {
		t.Fatal("Resume applied 0 entries after a genuine pre-kill commit of 2 turns -- the last committed turns were lost")
	}

	restartedRegistry := newAcceptanceJournaledAdapter(restartedStore, js)
	acceptanceJournalAssertRecovered(t, restartedRegistry, firstTurnID, secondTurnID)

	// Resume idempotency: a second replay of the same journal against the
	// now-restored store must produce delta=0 and no error ("exit 0"), and
	// the RPC poll surface must be unaffected: still exactly 2 turns, not 4.
	again, err := Resume(context.Background(), js, restartedStore, acceptanceJournalThread)
	if err != nil {
		t.Fatalf("second post-restart Resume: %v, want nil (exit 0)", err)
	}
	if again != 0 {
		t.Fatalf("second post-restart Resume applied = %d, want 0 -- the replay duplicated committed turns", again)
	}
	afterSecondResume, errObj := dispatch(t, restartedRegistry, MethodGetThread, getThreadParams{ThreadID: acceptanceJournalThread})
	if errObj != nil {
		t.Fatalf("get_thread after second Resume: %+v", errObj)
	}
	if n := len(afterSecondResume.(getThreadResult).Turns); n != 2 {
		t.Fatalf("get_thread after the idempotent second Resume returned %d turns, want 2 (unchanged)", n)
	}
}
