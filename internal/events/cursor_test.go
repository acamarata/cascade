// Purpose: white-box unit tests for loadCursor/commitCursor in isolation
//
//	from Bus — the "cursor miss" and malformed-value error paths task 6
//	calls for, plus the open-or-create-at-0 contract (task 3). In package
//	events (not events_test) because loadCursor/commitCursor are
//	unexported. R-14.117-authorized split from bus_test.go.
//
// SPORT: internal.events.Bus/ADDED (cursor tests) (P1-E03-W1-S04-T3).
package events

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestLoadCursor_OpenOrCreateAtZero(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()

	seq, err := loadCursor(ctx, store, "ns", "never-committed")
	if err != nil {
		t.Fatalf("loadCursor for a never-committed name returned an error: %v", err)
	}
	if seq != 0 {
		t.Fatalf("loadCursor for a never-committed name = %d, want 0", seq)
	}
}

func TestCommitCursor_ThenLoad_RoundTrips(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()

	if err := commitCursor(ctx, store, "ns", "consumer-a", 42); err != nil {
		t.Fatalf("commitCursor: %v", err)
	}
	seq, err := loadCursor(ctx, store, "ns", "consumer-a")
	if err != nil {
		t.Fatalf("loadCursor: %v", err)
	}
	if seq != 42 {
		t.Fatalf("loadCursor after commit = %d, want 42", seq)
	}

	// A later commit overwrites, never appends or requires the prior
	// value.
	if err := commitCursor(ctx, store, "ns", "consumer-a", 43); err != nil {
		t.Fatalf("second commitCursor: %v", err)
	}
	seq, err = loadCursor(ctx, store, "ns", "consumer-a")
	if err != nil {
		t.Fatalf("loadCursor after second commit: %v", err)
	}
	if seq != 43 {
		t.Fatalf("loadCursor after second commit = %d, want 43", seq)
	}
}

func TestLoadCursor_MalformedValue(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if err := store.Put(ctx, "ns", cursorKey("consumer-a"), []byte("short")); err != nil {
		t.Fatalf("seeding malformed cursor: %v", err)
	}

	_, err := loadCursor(ctx, store, "ns", "consumer-a")
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("loadCursor on malformed value = %v, want KindIntegrity", err)
	}
}

// cursorMissStore fails every Get with a non-NotFound error, proving
// loadCursor distinguishes a genuine "never committed" miss (KindNotFound,
// handled as 0/nil) from a real storage failure (surfaced, not swallowed).
type cursorMissStore struct {
	*storetest.MemStore
	getErr error
}

func (s *cursorMissStore) Get(ctx context.Context, namespace, key string) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.MemStore.Get(ctx, namespace, key)
}

func TestLoadCursor_StorageUnavailable(t *testing.T) {
	ctx := context.Background()
	injected := cascade.New(cascade.KindUnavailable, "injected: backend unreachable")
	store := &cursorMissStore{MemStore: storetest.NewMemStore(), getErr: injected}

	_, err := loadCursor(ctx, store, "ns", "consumer-a")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("loadCursor with a failing store = %v, want KindUnavailable", err)
	}
}

func TestUnsubscribe_CursorMiss(t *testing.T) {
	ctx := context.Background()
	bus := New(storetest.NewMemStore(), testkit.NewFrozenClock(time.Unix(1_700_000_000, 0)))
	t.Cleanup(func() { _ = bus.Close() })

	err := bus.Unsubscribe("ns", "never-subscribed")
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Unsubscribe on an unknown cursor = %v, want KindNotFound", err)
	}

	sub, subErr := bus.Subscribe(ctx, "ns", "consumer-a", 1)
	if subErr != nil {
		t.Fatalf("Subscribe: %v", subErr)
	}
	if unsubErr := sub.Unsubscribe(); unsubErr != nil {
		t.Fatalf("first Unsubscribe: %v", unsubErr)
	}
	// A second Unsubscribe of the same, now-inactive name is a miss too.
	if unsubErr := sub.Unsubscribe(); !cascade.HasKind(unsubErr, cascade.KindNotFound) {
		t.Fatalf("second Unsubscribe = %v, want KindNotFound", unsubErr)
	}
}

// TestEXE01LegacyAC6 proves concurrent allocation on one stream produces a
// gapless, monotonic StreamSeq: every publisher's sequence is distinct,
// the set is exactly 1..N, StreamSeq increases with Seq in the persisted
// log, and the recovered high-water mark equals N.
func TestEXE01LegacyAC6(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := New(store, clock)
	t.Cleanup(func() { _ = bus.Close() })

	const workers, perWorker = 8, 25
	got := publishConcurrently(ctx, t, bus, workers, perWorker)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	if len(got) != workers*perWorker {
		t.Fatalf("allocated %d sequence(s), want %d", len(got), workers*perWorker)
	}
	for i, s := range got {
		if s != uint64(i+1) {
			t.Fatalf("sorted stream seq[%d] = %d, want %d (gap or duplicate)", i, s, i+1)
		}
	}

	persisted, err := bus.ReplayStream(ctx, "ns", "task:one", 0)
	if err != nil {
		t.Fatalf("ReplayStream: %v", err)
	}
	for i, ev := range persisted {
		if ev.StreamSeq != uint64(i+1) || (i > 0 && ev.Seq <= persisted[i-1].Seq) {
			t.Fatalf("persisted event %d has (stream seq %d, seq %d), want stream seq %d with seq rising", i, ev.StreamSeq, ev.Seq, i+1)
		}
	}
	_, streams, err := recoverLog(ctx, store, "ns")
	if err != nil || streams["task:one"] != workers*perWorker {
		t.Fatalf("recoverLog high-water = %d, %v; want %d", streams["task:one"], err, workers*perWorker)
	}
}

// publishConcurrently has workers goroutines each publish perWorker events
// on the stream task:one and returns every StreamSeq the bus allocated.
func publishConcurrently(ctx context.Context, t *testing.T, bus *Bus, workers, perWorker int) []uint64 {
	t.Helper()
	seqs := make(chan uint64, workers*perWorker)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				ev, err := bus.PublishEvent(ctx, "ns", Event{Kind: "k", StreamID: "task:one"})
				if err != nil {
					t.Errorf("PublishEvent: %v", err)
					return
				}
				seqs <- ev.StreamSeq
			}
		}()
	}
	wg.Wait()
	close(seqs)

	var got []uint64
	for s := range seqs {
		got = append(got, s)
	}
	return got
}

// TestRecoverLog_LegacyRecordsJoinDefaultStream proves a record written
// before the stream extension recovers onto bus:<namespace> at its Seq, so
// the next Publish continues that stream without a gap.
func TestRecoverLog_LegacyRecordsJoinDefaultStream(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	for seq := uint64(1); seq <= 2; seq++ {
		full := encodeEvent(Event{Seq: seq, Kind: "k", Source: "s"})
		legacy := full[:len(full)-legacyExtensionSize()]
		if err := store.Put(ctx, "ns", eventKey(seq), legacy); err != nil {
			t.Fatalf("seeding legacy record: %v", err)
		}
	}
	bus := New(store, testkit.NewFrozenClock(time.Unix(1_700_000_000, 0)))
	t.Cleanup(func() { _ = bus.Close() })

	ev, err := bus.Publish(ctx, "ns", "k", "s", nil)
	if err != nil || ev.Seq != 3 || ev.StreamID != "bus:ns" || ev.StreamSeq != 3 {
		t.Fatalf("Publish after legacy records = %+v, %v; want seq 3 on bus:ns at 3", ev, err)
	}
}

// legacyExtensionSize is the byte width of an all-empty stream extension.
func legacyExtensionSize() int { return 4*envelopeLenSize + envelopeSeqSize + envelopeLenSize }
