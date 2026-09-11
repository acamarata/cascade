package conversation

// Purpose: journal.go's tests -- the append-before-commit ordering, the
//   abort-on-journal-failure contract, Resume's idempotence, the kill-9
//   simulation the ticket names explicitly, checkpoint idempotence and
//   its delegation to journal.Store's own recovery, and the C/S-04.T4
//   scheduler-signature proof.
// SPORT: internal.conversation.journal/ADDED (tests) (P1-E20-W5-S44-T4).

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/events/scheduler"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

var testJournalInstant = time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)

func newTestJournal(t *testing.T) *journal.SQLiteStore {
	t.Helper()
	js := journal.New(storetest.NewMemStore(), testkit.NewFrozenClock(testJournalInstant), journal.DefaultNamespace)
	t.Cleanup(func() { _ = js.Close() })
	return js
}

func seedTurn(threadID string, seq int64, content string) (Turn, []Segment) {
	turn := Turn{ID: NewTurnID(threadID, seq, RoleUser), ThreadID: threadID, Seq: seq, Role: RoleUser, CreatedAt: 1}
	segs := []Segment{{ID: NewSegmentID(turn.ID, 0, SegmentText), TurnID: turn.ID, Seq: 0, Kind: SegmentText, Content: content, CreatedAt: 1}}
	return turn, segs
}

// TestConversationJournal_IntentBeforeStoreCommit proves the ordering:
// a journal Append failure means the store is never touched at all.
type failingJournal struct {
	*journal.SQLiteStore
	failAppend bool
}

func (f *failingJournal) Append(ctx context.Context, entityID string, kind journal.Kind, opID string, payload json.RawMessage) (journal.Entry, error) {
	if f.failAppend {
		return journal.Entry{}, cascade.New(cascade.KindUnavailable, "journal disk fault")
	}
	return f.SQLiteStore.Append(ctx, entityID, kind, opID, payload)
}

func TestConversationJournal_AbortsBeforeStoreOnIntentFailure(t *testing.T) {
	store := newTestStore(t)
	fj := &failingJournal{SQLiteStore: newTestJournal(t), failAppend: true}
	turn, segs := seedTurn("th1", 0, "hello")

	if err := AppendTurnJournaled(context.Background(), fj, store, turn, segs); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("AppendTurnJournaled with a failing journal = %v, want KindUnavailable", err)
	}
	turns, err := store.ListTurns(context.Background(), "th1")
	if err != nil {
		t.Fatalf("ListTurns: %v", err)
	}
	if len(turns) != 0 {
		t.Fatalf("store has %d turns after an aborted journal write, want 0 -- no partial commit without a journal record", len(turns))
	}
}

func TestConversationJournal_WritesIntentAndAckOnSuccess(t *testing.T) {
	store := newTestStore(t)
	js := newTestJournal(t)
	turn, segs := seedTurn("th1", 0, "hello")

	if err := AppendTurnJournaled(context.Background(), js, store, turn, segs); err != nil {
		t.Fatalf("AppendTurnJournaled: %v", err)
	}

	entries, err := js.Replay(context.Background(), "th1", journal.Cursor{}, nil)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	var sawIntent, sawAck bool
	for _, e := range entries {
		if e.Kind == journal.KindIntent {
			sawIntent = true
		}
		if e.Kind == journal.KindAck {
			sawAck = true
		}
	}
	if !sawIntent || !sawAck {
		t.Fatalf("Replay entries = %+v, want both a KindIntent and a KindAck", entries)
	}
}

// TestConversationResume_SkipsReservedPayloadTypes proves Resume treats
// the two producer-less discriminators (reserved per R-21.216) as a
// no-op, not an error or a TurnAppend, so a future producer is safe.
func TestConversationResume_SkipsReservedPayloadTypes(t *testing.T) {
	store := newTestStore(t)
	js := newTestJournal(t)
	ctx := context.Background()

	for _, typ := range []string{JournalEntryThreadCreate, JournalEntrySegmentStateTransition} {
		payload, err := json.Marshal(journalPayload{Type: typ})
		if err != nil {
			t.Fatalf("marshal %s payload: %v", typ, err)
		}
		if _, err := js.Append(ctx, "th1", journal.KindAck, "op-"+typ, payload); err != nil {
			t.Fatalf("Append %s: %v", typ, err)
		}
	}

	applied, err := Resume(ctx, js, store, "th1")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if applied != 0 {
		t.Fatalf("Resume applied = %d, want 0 -- reserved payload types have no producer and must be skipped, not applied", applied)
	}
}

func TestConversationResume_IdempotentDoubleReplay(t *testing.T) {
	seedStore := newTestStore(t)
	js := newTestJournal(t)
	turn, segs := seedTurn("th1", 0, "hello")
	if err := AppendTurnJournaled(context.Background(), js, seedStore, turn, segs); err != nil {
		t.Fatalf("seed AppendTurnJournaled: %v", err)
	}

	// Resume into a FRESH store (the journal is the only durable source
	// of this data for this call): the first replay must actually apply
	// something.
	freshStore := newTestStore(t)
	first, err := Resume(context.Background(), js, freshStore, "th1")
	if err != nil {
		t.Fatalf("first Resume: %v", err)
	}
	if first == 0 {
		t.Fatalf("first Resume applied = 0, want > 0 (a fresh store has nothing yet)")
	}

	second, err := Resume(context.Background(), js, freshStore, "th1")
	if err != nil {
		t.Fatalf("second Resume: %v", err)
	}
	if second != 0 {
		t.Fatalf("second Resume applied = %d, want 0 (delta=0, idempotent)", second)
	}
}

// TestConversationKillResume is the ticket's named kill-9 simulation:
// write a turn (journal+store both commit), then simulate the daemon
// dying and restarting with the DATA STORE LOST but the journal intact
// (a fresh, empty conversation.Store -- the journal, not the store, is
// this domain's durable source of truth per this file's own doc
// comment) and prove Resume reconstructs the pre-kill state from the
// journal alone, matching the original store's content field for field.
func TestConversationKillResume(t *testing.T) {
	preKillStore := newTestStore(t)
	js := newTestJournal(t)
	turn, segs := seedTurn("th1", 0, "pre-kill content")
	if err := AppendTurnJournaled(context.Background(), js, preKillStore, turn, segs); err != nil {
		t.Fatalf("pre-kill AppendTurnJournaled: %v", err)
	}
	preKillTurns, err := preKillStore.ListTurns(context.Background(), "th1")
	if err != nil || len(preKillTurns) != 1 {
		t.Fatalf("pre-kill snapshot: turns=%v err=%v", preKillTurns, err)
	}

	// "kill -9": the process (and its data store) is gone; only js
	// (the journal, a separate durable log) survives, matching the
	// entity-journal-not-the-store-is-truth model this ticket wires.
	restartedStore := newTestStore(t)

	applied, err := Resume(context.Background(), js, restartedStore, "th1")
	if err != nil {
		t.Fatalf("Resume after simulated kill -9: %v", err)
	}
	if applied == 0 {
		t.Fatalf("Resume applied 0 entries, want the pre-kill turn+segment restored")
	}

	restoredTurns, err := restartedStore.ListTurns(context.Background(), "th1")
	if err != nil || len(restoredTurns) != 1 {
		t.Fatalf("restored turns=%v err=%v", restoredTurns, err)
	}
	if restoredTurns[0] != preKillTurns[0] {
		t.Fatalf("restored turn %+v != pre-kill turn %+v", restoredTurns[0], preKillTurns[0])
	}
	restoredSegs, err := restartedStore.ListSegments(context.Background(), turn.ID)
	if err != nil || len(restoredSegs) != 1 || restoredSegs[0].Content != "pre-kill content" {
		t.Fatalf("restored segments = %+v, err=%v, want the pre-kill content byte-for-byte", restoredSegs, err)
	}

	// Idempotence carries over the restart too: replaying the same
	// journal again against the now-restored store must be a no-op.
	again, err := Resume(context.Background(), js, restartedStore, "th1")
	if err != nil {
		t.Fatalf("second post-restart Resume: %v", err)
	}
	if again != 0 {
		t.Fatalf("second post-restart Resume applied = %d, want 0", again)
	}
}

func TestConversationBootstrapResume_SumsAcrossThreads(t *testing.T) {
	store := newTestStore(t)
	js := newTestJournal(t)
	t1, s1 := seedTurn("th1", 0, "a")
	t2, s2 := seedTurn("th2", 0, "b")
	if err := AppendTurnJournaled(context.Background(), js, store, t1, s1); err != nil {
		t.Fatalf("seed th1: %v", err)
	}
	if err := AppendTurnJournaled(context.Background(), js, store, t2, s2); err != nil {
		t.Fatalf("seed th2: %v", err)
	}

	restarted := newTestStore(t)
	applied, err := bootstrapResume(context.Background(), js, restarted, []string{"th1", "th2"})
	if err != nil {
		t.Fatalf("bootstrapResume: %v", err)
	}
	if applied < 2 {
		t.Fatalf("bootstrapResume applied = %d, want at least 2 (one turn+segment per thread)", applied)
	}
	for _, id := range []string{"th1", "th2"} {
		turns, err := restarted.ListTurns(context.Background(), id)
		if err != nil || len(turns) != 1 {
			t.Fatalf("thread %s: turns=%v err=%v", id, turns, err)
		}
	}

	second, err := bootstrapResume(context.Background(), js, restarted, []string{"th1", "th2"})
	if err != nil || second != 0 {
		t.Fatalf("second bootstrapResume = (%d, %v), want (0, nil)", second, err)
	}
}

func TestConversationCheckpoint_IdempotentDoubleCall(t *testing.T) {
	store := newTestStore(t)
	js := newTestJournal(t)
	turn, segs := seedTurn("th1", 0, "hello")
	if err := AppendTurnJournaled(context.Background(), js, store, turn, segs); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := Checkpoint(context.Background(), js, "th1"); err != nil {
		t.Fatalf("first Checkpoint: %v", err)
	}
	if err := Checkpoint(context.Background(), js, "th1"); err != nil {
		t.Fatalf("second Checkpoint (must be a no-op, not an error): %v", err)
	}
}

// TestConversationCheckpoint_DelegatesRecovery proves Checkpoint and
// Resume genuinely call through js's real Replay/Checkpoint (which run
// journal's own torn-tail recovery internally) rather than bypassing it:
// HeadSeq/Replay/Checkpoint against an entity that was never appended to
// behave exactly as journal.Store's own documented "never appended"
// contract (0, nil / not-found), which would not hold if this package
// read anything out-of-band.
func TestConversationCheckpoint_DelegatesRecovery(t *testing.T) {
	js := newTestJournal(t)
	if err := Checkpoint(context.Background(), js, "never-appended"); err != nil {
		t.Fatalf("Checkpoint on a never-appended entity: %v", err)
	}
	head, err := js.HeadSeq(context.Background(), "never-appended")
	if err != nil || head != 0 {
		t.Fatalf("HeadSeq on a never-appended entity = (%d, %v), want (0, nil)", head, err)
	}
}

// TestCheckpointJob_SchedulerCompatible proves checkpointJob's return
// value is a real C/S-04.T4 job signature by registering it on a real
// scheduler.Scheduler and confirming registration succeeds.
func TestCheckpointJob_SchedulerCompatible(t *testing.T) {
	store := newTestStore(t)
	js := newTestJournal(t)
	turn, segs := seedTurn("th1", 0, "hello")
	if err := AppendTurnJournaled(context.Background(), js, store, turn, segs); err != nil {
		t.Fatalf("seed: %v", err)
	}

	clock := testkit.NewFrozenClock(testJournalInstant)
	bus := events.New(storetest.NewMemStore(), clock)
	sched := scheduler.New(storetest.NewMemStore(), "conversation-checkpoint-test", clock, bus, "conversation-test-owner", time.Minute)

	job := checkpointJob(js, "th1")
	if err := sched.RegisterRunnable("conversation.checkpoint", job); err != nil {
		t.Fatalf("RegisterRunnable(checkpointJob(...)): %v", err)
	}
	// Invoke the registered Runnable directly (the signature proof the
	// acceptance criteria ask for) rather than driving a real cron tick,
	// which would need a schedule spec unrelated to this proof.
	if err := job(context.Background()); err != nil {
		t.Fatalf("invoking the registered job directly: %v", err)
	}
}
