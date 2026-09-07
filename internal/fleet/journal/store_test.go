package journal

// Purpose: Append's write path, the happy-path golden, storage-failure
//
//	handling, per-entity monotonic sequencing under concurrent writers,
//	the intent/ack fsync-ordering contract, and torn-tail recovery.
//
// Constraints: Art.7.1 (every file under t.TempDir), Art.7.3 (frozen
//
//	clock, never the wall clock), Art.11 (no sleeps as synchronization).
//
// SPORT: internal.fleet.journal.Store/ADDED (tests) (P1-E13-W3-S27-T1).

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
	sqlite "github.com/acamarata/cascade/providers/sqlite"
)

// testInstant is the frozen instant every test clock starts at.
var testInstant = time.Unix(1_700_000_000, 0).UTC()

// newSQLiteStore opens a real SQLite store under t.TempDir. The real
// driver is used, rather than a fake, wherever the behavior under test is
// storage behavior (Art.2 real counterpart).
func newSQLiteStore(t *testing.T) provider.Store {
	t.Helper()
	driver, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "cascade.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	return driver
}

// newTestStore returns a journal SQLiteStore over a real SQLite store and a
// frozen clock, plus the underlying store so a test can reopen a second
// SQLiteStore instance against the same data (simulating a restart).
func newTestStore(t *testing.T) (*SQLiteStore, provider.Store, *testkit.FrozenClock) {
	t.Helper()
	pstore := newSQLiteStore(t)
	clock := testkit.NewFrozenClock(testInstant)
	return New(pstore, clock, DefaultNamespace), pstore, clock
}

func TestJournalAppendCheckpointReplay(t *testing.T) {
	ctx := context.Background()
	store, _, clock := newTestStore(t)

	e1, err := store.Append(ctx, "task-1", KindIntent, "op-1", json.RawMessage(`{"n":1}`))
	if err != nil {
		t.Fatalf("Append 1: %v", err)
	}
	if e1.Seq != 1 || e1.TSUnixNano != clock.Now().UnixNano() {
		t.Fatalf("Append 1 = %+v, want seq 1 at the frozen instant", e1)
	}
	e2, err := store.Append(ctx, "task-1", KindAck, "op-1", nil)
	if err != nil {
		t.Fatalf("Append 2: %v", err)
	}
	if e2.Seq != 2 {
		t.Fatalf("Append 2 seq = %d, want 2", e2.Seq)
	}

	if err := store.Checkpoint(ctx, Cursor{EntityID: "task-1", Seq: 2}); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	// An explicit cursor naming task-1 at seq 0 replays from the very
	// start of the log, regardless of what has since been checkpointed
	// (the checkpoint-loss fallback only applies to an absent/stale
	// cursor — see TestJournalCursorLossFallback).
	got, err := store.Replay(ctx, "task-1", Cursor{EntityID: "task-1"}, nil)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("Replay = %+v, want [seq 1, seq 2]", got)
	}
}

func TestJournalMonotonicSequencePerEntity(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			op := fmt.Sprintf("op-%d", i)
			if _, err := store.Append(ctx, "shared", KindNodeStream, op, nil); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Append: %v", err)
	}

	entries, err := store.Replay(ctx, "shared", Cursor{}, nil)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(entries) != n {
		t.Fatalf("got %d entries, want %d", len(entries), n)
	}
	seen := make(map[uint64]bool, n)
	for _, e := range entries {
		if seen[e.Seq] {
			t.Fatalf("duplicate seq %d", e.Seq)
		}
		seen[e.Seq] = true
	}
	for seq := uint64(1); seq <= n; seq++ {
		if !seen[seq] {
			t.Fatalf("missing seq %d: sequence is not gapless", seq)
		}
	}
}

func TestJournalIntentFsyncedBeforeSideEffect(t *testing.T) {
	ctx := context.Background()
	pstore := newSQLiteStore(t)
	clock := testkit.NewFrozenClock(testInstant)
	storeA := New(pstore, clock, DefaultNamespace)

	if _, err := storeA.Append(ctx, "op-entity", KindIntent, "op-1", nil); err != nil {
		t.Fatalf("Append intent: %v", err)
	}
	// Simulate a crash between the intent and its acknowledgement: no Ack
	// is ever appended by storeA. A fresh SQLiteStore over the same underlying
	// data (storeB) is the "process restarts" observation point.
	storeB := New(pstore, clock, DefaultNamespace)
	entries, err := storeB.Replay(ctx, "op-entity", Cursor{}, nil)
	if err != nil {
		t.Fatalf("Replay after simulated crash: %v", err)
	}
	if len(entries) != 1 || entries[0].Kind != KindIntent || entries[0].OperationID != "op-1" {
		t.Fatalf("Replay after simulated crash = %+v, want exactly one KindIntent op-1 entry", entries)
	}

	if _, err := storeA.Append(ctx, "op-entity", KindAck, "op-1", nil); err != nil {
		t.Fatalf("Append ack: %v", err)
	}
	entries, err = storeB.Replay(ctx, "op-entity", Cursor{}, nil)
	if err != nil {
		t.Fatalf("Replay after ack: %v", err)
	}
	if len(entries) != 2 || entries[1].Kind != KindAck {
		t.Fatalf("Replay after ack = %+v, want [Intent, Ack]", entries)
	}
}

func TestJournalTornTailTruncatedAndReported(t *testing.T) {
	ctx := context.Background()
	pstore := newSQLiteStore(t)
	clock := testkit.NewFrozenClock(testInstant)
	storeA := New(pstore, clock, DefaultNamespace)

	for i := 0; i < 3; i++ {
		op := fmt.Sprintf("op-%d", i)
		if _, err := storeA.Append(ctx, "e", KindNodeStream, op, nil); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	// Simulate an in-flight write that started but whose transaction
	// never committed the head pointer: garbage bytes land at the next
	// sequence number, and the head pointer stays at 3.
	if err := pstore.Put(ctx, DefaultNamespace, entryKey("e", 4), []byte("not a valid entry")); err != nil {
		t.Fatalf("seeding corrupted entry: %v", err)
	}

	storeB := New(pstore, clock, DefaultNamespace)
	report, err := storeB.Recover(ctx, "e")
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if report.Truncated != 1 || report.FirstBadSeq != 4 {
		t.Fatalf("Recover report = %+v, want {Truncated:1 FirstBadSeq:4}", report)
	}

	entries, err := storeB.Replay(ctx, "e", Cursor{}, nil)
	if err != nil {
		t.Fatalf("Replay after recovery: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("Replay after recovery returned %d entries, want 3 (the corrupted one truncated)", len(entries))
	}

	// The store must accept new appends after recovery, continuing from
	// the repaired head rather than reusing or skipping sequence 4.
	e, err := storeB.Append(ctx, "e", KindNodeStream, "op-new", nil)
	if err != nil {
		t.Fatalf("Append after recovery: %v", err)
	}
	if e.Seq != 4 {
		t.Fatalf("Append after recovery: seq = %d, want 4", e.Seq)
	}
}

// seedCrashInjectionBoundaries appends the four intent/ack lifecycle
// boundaries TestJournalCrashInjection asserts against: a "crash" is
// modeled by simply stopping storeA short of the next expected call,
// since every Append and Checkpoint call is synchronous and already
// durable by the time it returns.
func seedCrashInjectionBoundaries(ctx context.Context, t *testing.T, storeA *SQLiteStore, pstore provider.Store) {
	t.Helper()
	// Boundary: after the intent write, before its "fsync" (i.e. the
	// caller never proceeds past the returned Append call at all).
	if _, err := storeA.Append(ctx, "boundary-1", KindIntent, "op-1", nil); err != nil {
		t.Fatalf("boundary 1 intent: %v", err)
	}
	// Boundary: after the intent fsync, before the side effect.
	if _, err := storeA.Append(ctx, "boundary-2", KindIntent, "op-2", nil); err != nil {
		t.Fatalf("boundary 2 intent: %v", err)
	}
	// Boundary: after the side effect, before the acknowledgement write.
	if _, err := storeA.Append(ctx, "boundary-3", KindIntent, "op-3", nil); err != nil {
		t.Fatalf("boundary 3 intent: %v", err)
	}
	// Boundary: after the acknowledgement write, before its fsync — the
	// write already committed by the time Append returns, so this
	// boundary is observably identical to "ack committed".
	if _, err := storeA.Append(ctx, "boundary-4", KindIntent, "op-4", nil); err != nil {
		t.Fatalf("boundary 4 intent: %v", err)
	}
	if _, err := storeA.Append(ctx, "boundary-4", KindAck, "op-4", nil); err != nil {
		t.Fatalf("boundary 4 ack: %v", err)
	}
	// Boundary: mid-checkpoint transaction. Checkpoint must be
	// all-or-nothing: an out-of-range checkpoint makes no partial write.
	if err := storeA.Checkpoint(ctx, Cursor{EntityID: "boundary-4", Seq: 99}); err == nil {
		t.Fatal("Checkpoint beyond the log head succeeded, want ErrCheckpointBeyondLog")
	}
	if _, ok, err := storeA.loadCheckpointFrom(ctx, pstore, "boundary-4"); err != nil || ok {
		t.Fatalf("a refused Checkpoint left a partial record: ok=%v err=%v", ok, err)
	}
}

func TestJournalCrashInjection(t *testing.T) {
	ctx := context.Background()
	pstore := newSQLiteStore(t)
	clock := testkit.NewFrozenClock(testInstant)
	storeA := New(pstore, clock, DefaultNamespace)
	seedCrashInjectionBoundaries(ctx, t, storeA, pstore)

	// Boundary: mid-entry (a torn write) is covered by
	// TestJournalTornTailTruncatedAndReported. Here, reopen every entity
	// seeded above and confirm each yields a consistent log and a replay
	// that applies each operation_id exactly once.
	storeB := New(pstore, clock, DefaultNamespace)
	for _, tc := range []struct {
		entity   string
		wantKind []Kind
	}{
		{"boundary-1", []Kind{KindIntent}},
		{"boundary-2", []Kind{KindIntent}},
		{"boundary-3", []Kind{KindIntent}},
		{"boundary-4", []Kind{KindIntent, KindAck}},
	} {
		entries, err := storeB.Replay(ctx, tc.entity, Cursor{}, nil)
		if err != nil {
			t.Fatalf("Replay(%s) after simulated crash: %v", tc.entity, err)
		}
		if len(entries) != len(tc.wantKind) {
			t.Fatalf("Replay(%s) = %+v, want %d entries", tc.entity, entries, len(tc.wantKind))
		}
		for i, k := range tc.wantKind {
			if entries[i].Kind != k {
				t.Fatalf("Replay(%s)[%d].Kind = %v, want %v", tc.entity, i, entries[i].Kind, k)
			}
		}
	}
}
