package conversation

// Purpose: retention.go's own tests -- zero-window no-op, exact
//   tombstoned row count (verified against the real
//   conversation_turn_tombstone table, not just PruneResult's own
//   number), idempotent double prune (delta=0 on the second call), and
//   the scheduler-entry-point signature-compatibility proof this
//   ticket's AC names explicitly, driven against a REAL
//   internal/events/scheduler.Scheduler (never a fake).
// SPORT: internal.conversation.retention/ADDED (tests) (P1-E20-W5-S44-T3).

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/events/routing"
	"github.com/acamarata/cascade/internal/events/scheduler"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

// allowAllGate is this test's own scheduler.ActionGate: every scheduler in
// this package's test files must install one to fire at all (a nil gate
// fails closed, scheduler_route.go), matching that package's own
// newTestSchedulerTTL precedent (an unexported test helper this package
// cannot import).
type allowAllGate struct{}

func (allowAllGate) RouteAction(context.Context, routing.Action) (policy.Verdict, policy.Trace, error) {
	return policy.VerdictAllow, policy.Trace{}, nil
}

// newTestStoreWithDB is newTestStore (domain_test.go) plus the raw *sql.DB
// handle, for tests that verify tombstone rows directly against the
// table rather than trusting PruneResult's own count alone.
//
// The DSN skips journal fsyncs (see openTestDB): the database is
// single-connection, single-process and discarded at cleanup.
func newTestStoreWithDB(t *testing.T) (Store, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "retention-test.db")
	dsn := path + "?_journal_mode=MEMORY&_synchronous=OFF"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := ApplyConversationSchema(context.Background(), db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyConversationSchema: %v", err)
	}
	return NewStore(db), db
}

func countTombstones(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + tableTurnTombstone).Scan(&n); err != nil {
		t.Fatalf("countTombstones: %v", err)
	}
	return n
}

// seedAgedTurns appends n turns to threadID, all at CreatedAt=100 (old
// relative to any realistic now/AgeMaxDays used below).
func seedAgedTurns(t *testing.T, s Store, threadID string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		turn := Turn{ID: NewTurnID(threadID, int64(i), RoleUser), ThreadID: threadID, Seq: int64(i), Role: RoleUser, CreatedAt: 100}
		if err := s.AppendTurn(context.Background(), turn); err != nil {
			t.Fatalf("seedAgedTurns(%d): %v", i, err)
		}
	}
}

// THE NAMES ARE THE CHECK. This ticket's contract runs
//   go test ./internal/conversation/... -run '^TestRetention'
// and these cases were originally called TestPruneTurns_*, which that
// pattern does not match: the check reported "[no tests to run]" and passed
// while proving nothing. Renaming them is what makes the contract's own
// check execute. A future rename must keep the TestRetention prefix.

func TestRetentionZeroWindowIsANoOp(t *testing.T) {
	s, db := newTestStoreWithDB(t)
	seedAgedTurns(t, s, "th-zero", 5)

	for name, policy := range map[string]RetentionPolicy{
		"zero-age":   {AgeMaxDays: 0, TurnsMaxPerThread: 1},
		"zero-count": {AgeMaxDays: 1, TurnsMaxPerThread: 0},
		"both-zero":  {},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := s.PruneTurns(context.Background(), policy, 1_000_000)
			if err != nil {
				t.Fatalf("PruneTurns(%+v): %v", policy, err)
			}
			if res.Tombstoned != 0 {
				t.Fatalf("PruneTurns(%+v).Tombstoned = %d, want 0 (zero-window no-op)", policy, res.Tombstoned)
			}
		})
	}
	if n := countTombstones(t, db); n != 0 {
		t.Fatalf("conversation_turn_tombstone has %d rows, want 0", n)
	}
}

func TestRetentionTombstonesExactlyTheQualifyingRowsAndIsIdempotent(t *testing.T) {
	s, db := newTestStoreWithDB(t)
	seedAgedTurns(t, s, "th-prune", 5) // seq 0..4, all CreatedAt=100

	policy := RetentionPolicy{AgeMaxDays: 1, TurnsMaxPerThread: 2}
	now := int64(1_000_000) // cutoff = now - 86400, comfortably above CreatedAt=100

	res, err := s.PruneTurns(context.Background(), policy, now)
	if err != nil {
		t.Fatalf("PruneTurns (first call): %v", err)
	}
	// Turns 0,1,2 each have >=2 newer siblings (turns 3,4 or more) and
	// are older than cutoff: exactly 3 qualify. Turns 3,4 are within the
	// most-recent 2 and never qualify.
	if res.Tombstoned != 3 {
		t.Fatalf("PruneTurns (first call).Tombstoned = %d, want 3", res.Tombstoned)
	}
	if n := countTombstones(t, db); n != 3 {
		t.Fatalf("conversation_turn_tombstone has %d rows after first prune, want 3 (real table state)", n)
	}

	// Turn data itself is never deleted or modified by a logical prune.
	turns, err := s.ListTurns(context.Background(), "th-prune")
	if err != nil || len(turns) != 5 {
		t.Fatalf("ListTurns after prune = %+v, %v, want all 5 turns still present", turns, err)
	}

	// MUTATION PROOF (idempotency): a second call against UNCHANGED data
	// must tombstone zero additional rows -- the real, falsifiable
	// assertion is the row count staying at 3, not merely
	// res2.Tombstoned == 0 in isolation.
	res2, err := s.PruneTurns(context.Background(), policy, now)
	if err != nil {
		t.Fatalf("PruneTurns (second call): %v", err)
	}
	if res2.Tombstoned != 0 {
		t.Fatalf("PruneTurns (second call).Tombstoned = %d, want 0 (idempotent)", res2.Tombstoned)
	}
	if n := countTombstones(t, db); n != 3 {
		t.Fatalf("conversation_turn_tombstone has %d rows after second prune, want still 3", n)
	}
}

// TestRetentionRegistersAsASchedulerJob proves PruneTurns's shape
// is accepted by a REAL internal/events/scheduler.Scheduler
// (RegisterRunnable + ScheduleJob + Tick), never a fake scheduler double
// -- see retention.go's own FINDING doc comment on why this test, not a
// live composition-root registration, is what discharges this ticket's
// AC ("standalone invocation test with a test scheduler").
func TestRetentionRegistersAsASchedulerJob(t *testing.T) {
	s, db := newTestStoreWithDB(t)
	seedAgedTurns(t, s, "th-sched", 3)

	memStore := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	bus := events.New(memStore, clock)
	sched := scheduler.New(memStore, "queue", clock, bus, "test-owner", 1000*time.Hour)
	if err := sched.SetActionGate(allowAllGate{}, policy.Subject{Kind: policy.SubjectAgent, ID: "scheduler"}, "scheduler.dispatch"); err != nil {
		t.Fatalf("SetActionGate: %v", err)
	}

	prunePolicy := RetentionPolicy{AgeMaxDays: 1, TurnsMaxPerThread: 1}
	const owner = "conversation-retention-prune"
	if err := sched.RegisterRunnable(owner, func(ctx context.Context) error {
		_, err := s.PruneTurns(ctx, prunePolicy, clock.Now().Unix())
		return err
	}); err != nil {
		t.Fatalf("RegisterRunnable(PruneTurns closure): %v", err)
	}
	if err := sched.ScheduleJob(context.Background(), "conversation-retention", "@every 168h", owner); err != nil {
		t.Fatalf("ScheduleJob: %v", err)
	}
	// Skip-missed scheduling computes the next occurrence strictly after
	// clock.Now() AT Activate time, so the clock must advance PAST that
	// point (interval + margin) before a subsequent Tick fires anything.
	if _, err := sched.Activate(context.Background()); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	t.Cleanup(func() { _ = sched.Close(context.Background()) })
	clock.Advance(169 * time.Hour)
	report, err := sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("Tick report.Errors = %v, want the PruneTurns runnable to fire cleanly", report.Errors)
	}
	if n := countTombstones(t, db); n != 2 {
		t.Fatalf("conversation_turn_tombstone has %d rows after the scheduler fired PruneTurns, want 2", n)
	}
}

// TestRetentionAdapterRoundTrip drives the Go-level Adapter surface.
func TestRetentionAdapterRoundTrip(t *testing.T) {
	adapter, registry, _ := newTestAdapter(t)
	for i := 0; i < 3; i++ {
		if _, errObj := dispatch(t, registry, MethodAppendTurn, appendTurnParams{
			ThreadID: "th-adapter-prune", Role: "user", Segments: []appendSegmentWire{{Kind: "text", Content: "hi"}},
		}); errObj != nil {
			t.Fatalf("append_turn(%d): %+v", i, errObj)
		}
	}
	res, err := adapter.PruneTurns(context.Background(), RetentionPolicy{AgeMaxDays: 0, TurnsMaxPerThread: 0})
	if err != nil || res.Tombstoned != 0 {
		t.Fatalf("Adapter.PruneTurns(zero-window) = %+v, %v, want a no-op", res, err)
	}
}
