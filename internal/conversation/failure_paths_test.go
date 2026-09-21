package conversation

// Purpose: the storage and journal failure paths this package's happy-path
//   suites never reach -- every one of them driven by a REAL failure of the
//   real collaborator (a closed sqlite handle, a dropped table, a journal
//   whose Append or Replay refuses), never by a fake of the code under
//   test. Security-class coverage floors (Art.4) count the whole package,
//   and an untested error branch in the store is a branch the scrub
//   pipeline's fail-closed path depends on.
// SPORT: internal.conversation.store (CHANGED -- failure-path tests),
//   internal.conversation.journal (CHANGED -- failure-path tests)
//   (P1-E20-W5-S44-T1).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// newClosedStore returns a Store whose real database handle is closed, so
// every statement it issues fails for real inside the driver.
func newClosedStore(t *testing.T) Store {
	t.Helper()
	db := openTestDB(t)
	if err := ApplyConversationSchema(context.Background(), db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyConversationSchema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the test db: %v", err)
	}
	return NewStore(db)
}

// TestStore_EveryReadAndWriteFailsClosedOnADeadHandle walks the whole Store
// surface against a closed handle. Each call must return an error: a read
// that answered "empty" here would report a thread with no turns, and a
// write that answered nil would report a turn as appended.
func TestStore_EveryReadAndWriteFailsClosedOnADeadHandle(t *testing.T) {
	ctx := context.Background()
	store := newClosedStore(t)
	turn := Turn{ID: "t1", ThreadID: "th1", Seq: 0, Role: RoleUser, CreatedAt: 1}
	seg := Segment{ID: "s1", TurnID: "t1", Seq: 0, Kind: SegmentText, Content: "x", CreatedAt: 1}

	calls := map[string]func() error{
		"AppendTurn":    func() error { return store.AppendTurn(ctx, turn) },
		"AppendSegment": func() error { return store.AppendSegment(ctx, seg) },
		"GetThread": func() error {
			_, _, err := store.GetThread(ctx, "th1")
			return err
		},
		"ListTurns":    func() error { _, err := store.ListTurns(ctx, "th1"); return err },
		"ListSegments": func() error { _, err := store.ListSegments(ctx, "t1"); return err },
		"ListThreads":  func() error { _, err := store.ListThreads(ctx); return err },
		"ListTurnsPage": func() error {
			_, err := store.ListTurnsPage(ctx, "th1", PaginationFilter{})
			return err
		},
		"ListThreadsPage": func() error { _, err := store.ListThreadsPage(ctx, PaginationFilter{}); return err },
		"SearchTurns":     func() error { _, err := store.SearchTurns(ctx, "x", SearchFilter{}); return err },
		"ArchiveThread":   func() error { return store.ArchiveThread(ctx, "th1", 2) },
		"UnarchiveThread": func() error { return store.UnarchiveThread(ctx, "th1") },
		"IsArchived":      func() error { _, err := store.IsArchived(ctx, "th1"); return err },
		"PruneTurns": func() error {
			_, err := store.PruneTurns(ctx, RetentionPolicy{AgeMaxDays: 1, TurnsMaxPerThread: 1}, 100)
			return err
		},
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s on a closed handle returned nil; a dead store must refuse, not answer", name)
		}
	}
}

// TestStore_AppendSegmentRefusesAMissingTurn covers AppendSegment's
// turn-existence branch against the real schema.
func TestStore_AppendSegmentRefusesAMissingTurn(t *testing.T) {
	store := newTestStore(t)
	seg := Segment{ID: "s1", TurnID: "nope", Seq: 0, Kind: SegmentText, Content: "x", CreatedAt: 1}
	if err := store.AppendSegment(context.Background(), seg); err != ErrTurnNotFound {
		t.Fatalf("AppendSegment onto a missing turn = %v, want ErrTurnNotFound", err)
	}
}

// TestTranslateAppendError_ClassifiesRealDriverErrors pins the classifier
// on errors the real driver produced: a duplicate primary key (a genuine
// SQLITE_CONSTRAINT) is ErrImmutable, and any other driver failure is a
// storage failure rather than being reported as an immutability conflict.
// Identity comparison, not errors.Is: a cascade error's Is compares the
// KIND only, so errors.Is would accept any KindConflict error here.
func TestTranslateAppendError_ClassifiesRealDriverErrors(t *testing.T) {
	db := openTestDB(t)
	if err := ApplyConversationSchema(context.Background(), db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyConversationSchema: %v", err)
	}
	insert := `INSERT INTO ` + tableThread + ` (id, name, created_at) VALUES ('dup','dup',1)`
	if _, err := db.Exec(insert); err != nil {
		t.Fatalf("seed a thread row: %v", err)
	}
	_, dupErr := db.Exec(insert)
	if dupErr == nil {
		t.Fatal("a duplicate primary key was accepted; the schema lost its PRIMARY KEY")
	}
	if got := translateAppendError(dupErr, "conversation: test"); got != ErrImmutable {
		t.Fatalf("translateAppendError(real constraint violation) = %v, want ErrImmutable", got)
	}

	_, missingTable := db.Exec(`INSERT INTO conversation_no_such_table (id) VALUES ('x')`)
	if missingTable == nil {
		t.Fatal("an insert into a nonexistent table succeeded")
	}
	got := translateAppendError(missingTable, "conversation: test")
	if got == ErrImmutable {
		t.Fatal("a non-constraint driver failure was reported as an immutability conflict")
	}
	if !cascade.HasKind(got, cascade.KindUnavailable) {
		t.Fatalf("translateAppendError(non-constraint) = %v, want KindUnavailable", got)
	}
	if translateAppendError(nil, "conversation: test") != nil {
		t.Fatal("translateAppendError(nil) returned an error")
	}
}

// selectiveJournal fails Append for the kinds named, or Replay, and passes
// everything else through to the real journal store. The journal is a
// collaborator of the code under test, not the code under test.
type selectiveJournal struct {
	*journal.SQLiteStore
	failKind    journal.Kind // zero is not a member of the enum, so it means "fail nothing"
	failReplay  bool
	failHeadSeq bool
}

func (s *selectiveJournal) Append(ctx context.Context, entityID string, kind journal.Kind,
	opID string, payload json.RawMessage) (journal.Entry, error) {
	if s.failKind != 0 && kind == s.failKind {
		return journal.Entry{}, cascade.New(cascade.KindUnavailable, "test: journal refuses this kind")
	}
	return s.SQLiteStore.Append(ctx, entityID, kind, opID, payload)
}

func (s *selectiveJournal) Replay(ctx context.Context, entityID string, cursor journal.Cursor,
	kinds []journal.Kind) ([]journal.Entry, error) {
	if s.failReplay {
		return nil, cascade.New(cascade.KindUnavailable, "test: journal refuses to replay")
	}
	return s.SQLiteStore.Replay(ctx, entityID, cursor, kinds)
}

func (s *selectiveJournal) HeadSeq(ctx context.Context, entityID string) (uint64, error) {
	if s.failHeadSeq {
		return 0, cascade.New(cascade.KindUnavailable, "test: journal cannot read its head")
	}
	return s.SQLiteStore.HeadSeq(ctx, entityID)
}

// TestAppendTurnJournaled_AckFailureIsReportedAfterTheCommit covers the
// path the happy-path tests skip: the store commit succeeded and the ack
// entry did not. The caller must be told, and the committed rows must
// still be there -- the intent entry is what makes that state recoverable.
func TestAppendTurnJournaled_AckFailureIsReportedAfterTheCommit(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	js := &selectiveJournal{SQLiteStore: newTestJournal(t), failKind: journal.KindAck}
	turn, segs := seedTurn("th1", 0, "hello")

	err := AppendTurnJournaled(ctx, js, store, turn, segs)
	if err == nil {
		t.Fatal("AppendTurnJournaled returned nil when the ack write failed")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("ack failure = %v, want KindUnavailable", err)
	}
	turns, listErr := store.ListTurns(ctx, "th1")
	if listErr != nil || len(turns) != 1 {
		t.Fatalf("ListTurns = %v, %v, want the committed turn to still be there", turns, listErr)
	}
}

// TestAppendTurnJournaled_StoreFailurePropagates covers the two store
// branches between the intent and the ack: a refused turn and a refused
// segment both abort before the ack is written.
func TestAppendTurnJournaled_StoreFailurePropagates(t *testing.T) {
	ctx := context.Background()
	turn, segs := seedTurn("th1", 0, "hello")

	if err := AppendTurnJournaled(ctx, newTestJournal(t), newClosedStore(t), turn, segs); err == nil {
		t.Fatal("AppendTurnJournaled over a dead store returned nil")
	}

	store := newTestStore(t)
	bad := []Segment{{ID: "", TurnID: turn.ID, Seq: 0, Kind: SegmentText, Content: "x", CreatedAt: 1}}
	if err := AppendTurnJournaled(ctx, newTestJournal(t), store, turn, bad); err != ErrInvalidRecord {
		t.Fatalf("AppendTurnJournaled with an unstorable segment = %v, want ErrInvalidRecord", err)
	}
}

// TestCheckpoint_HeadSeqFailurePropagates covers Checkpoint's read half.
func TestCheckpoint_HeadSeqFailurePropagates(t *testing.T) {
	js := &selectiveJournal{SQLiteStore: newTestJournal(t), failHeadSeq: true}
	if err := Checkpoint(context.Background(), js, "th1"); err == nil {
		t.Fatal("Checkpoint returned nil when the journal could not read its head")
	}
}

// TestResume_FailurePaths covers Resume's three error branches: a refused
// Replay, an entry whose payload does not decode, and a store that refuses
// a replayed turn for a reason other than "already applied".
func TestResume_FailurePaths(t *testing.T) {
	ctx := context.Background()

	t.Run("ReplayRefused", func(t *testing.T) {
		js := &selectiveJournal{SQLiteStore: newTestJournal(t), failReplay: true}
		if _, err := Resume(ctx, js, newTestStore(t), "th1"); err == nil {
			t.Fatal("Resume returned nil when Replay refused")
		}
	})

	t.Run("UndecodablePayload", func(t *testing.T) {
		js := newTestJournal(t)
		if _, err := js.Append(ctx, "th1", journal.KindAck, "op-1", json.RawMessage(`"not an object"`)); err != nil {
			t.Fatalf("seed a journal entry: %v", err)
		}
		if _, err := Resume(ctx, js, newTestStore(t), "th1"); err != ErrJournalDecodeFailed {
			t.Fatalf("Resume over an undecodable payload = %v, want ErrJournalDecodeFailed", err)
		}
	})

	t.Run("StoreRefusesAReplayedTurn", func(t *testing.T) {
		js := newTestJournal(t)
		turn, segs := seedTurn("th1", 0, "hello")
		if err := AppendTurnJournaled(ctx, js, newTestStore(t), turn, segs); err != nil {
			t.Fatalf("seed a journaled turn: %v", err)
		}
		if _, err := Resume(ctx, js, newClosedStore(t), "th1"); err == nil {
			t.Fatal("Resume returned nil replaying into a dead store")
		}
	})
}

// TestResume_SkipsEntriesThatAreNotTurnAppends covers the discriminator
// branch: an acked entry of another payload type is skipped, not applied.
func TestResume_SkipsEntriesThatAreNotTurnAppends(t *testing.T) {
	ctx := context.Background()
	js := newTestJournal(t)
	payload, err := json.Marshal(journalPayload{Type: JournalEntryThreadCreate})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := js.Append(ctx, "th1", journal.KindAck, "op-1", payload); err != nil {
		t.Fatalf("seed a journal entry: %v", err)
	}
	applied, err := Resume(ctx, js, newTestStore(t), "th1")
	if err != nil || applied != 0 {
		t.Fatalf("Resume = %d, %v, want 0, nil for a non-turn-append entry", applied, err)
	}
}

// TestNewThreadID_MintsADistinctPrefixedID covers the mint path. Its error
// branch is not reachable from a test: cascade.NewID reads crypto/rand
// directly, with no injectable reader, and a crypto/rand.Read failure is
// not a condition a test can create on a supported platform. Recorded here
// rather than papered over with a fake ID minter.
func TestNewThreadID_MintsADistinctPrefixedID(t *testing.T) {
	first, err := NewThreadID()
	if err != nil {
		t.Fatalf("NewThreadID: %v", err)
	}
	second, err := NewThreadID()
	if err != nil {
		t.Fatalf("NewThreadID: %v", err)
	}
	if first == second {
		t.Fatalf("two NewThreadID calls both returned %q", first)
	}
	if len(first) <= len("thread-") || first[:len("thread-")] != "thread-" {
		t.Fatalf("NewThreadID = %q, want a thread- prefixed id", first)
	}
}
