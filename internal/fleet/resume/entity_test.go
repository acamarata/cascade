// Purpose: FanOutEntity and its inverse round-trip, and the inverse
//   refuses every non-fan-out entity id; per-leg attempts of one fan-out
//   entity stay unique and capped across adapters sharing one store, and a
//   conflict that leaves a slot absent neither skips it nor spends the cap.
// SPORT: internal.fleet.resume.ResumeManager/CHANGE (tests) (P1-CORE-18).

package resume

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestFanOutEntityRoundTrip(t *testing.T) {
	for _, id := range []string{"01J9Z8Q", "a", "fanout:nested"} {
		entity := FanOutEntity(id)
		if entity != "fanout:"+id {
			t.Fatalf("FanOutEntity(%q) = %q, want %q", id, entity, "fanout:"+id)
		}
		got, ok := FanOutIDFromEntity(entity)
		if !ok || got != id {
			t.Fatalf("FanOutIDFromEntity(%q) = (%q, %v), want (%q, true)", entity, got, ok, id)
		}
	}
	for _, entity := range []string{"", "fanout:", "task-1", "Fanout:x", "xfanout:y"} {
		if got, ok := FanOutIDFromEntity(entity); ok || got != "" {
			t.Fatalf("FanOutIDFromEntity(%q) = (%q, %v), want (\"\", false)", entity, got, ok)
		}
	}
}

// slotBarrier wraps the provider.Store adapters allocate from. The first
// two plain Gets of an attempt-1 slot wait until both have read, so a
// claim that reads a slot and then writes it outside one CompareAndSwap
// lets both contenders see it absent and both take attempt 1. A create-only
// claim only reads a slot after losing it, so the barrier cannot change it.
type slotBarrier struct {
	provider.Store
	t     *testing.T
	reads atomic.Int32
	ready chan struct{}
}

func (b *slotBarrier) Get(ctx context.Context, ns, key string) ([]byte, error) {
	v, err := b.Store.Get(ctx, ns, key)
	if ns == legAttemptsNamespace && strings.HasSuffix(key, "#1") {
		if n := b.reads.Add(1); n == 2 {
			close(b.ready)
		} else if n < 2 {
			select {
			case <-b.ready:
			case <-time.After(10 * time.Second):
				b.t.Error("slot barrier: no second contender read slot 1")
			}
		}
	}
	return v, err
}

// rawStarts counts every stored fanout_leg_started entry of id's entity,
// read below Replay's (kind, operation_id) dedupe.
func rawStarts(t *testing.T, raw provider.Store, id string) int {
	t.Helper()
	it, err := raw.Scan(context.Background(), journal.DefaultNamespace, "e:"+FanOutEntity(id)+"\x00")
	if err != nil {
		t.Fatalf("scan raw journal: %v", err)
	}
	defer func() { _ = it.Close() }()
	n := 0
	for it.Next(context.Background()) {
		var e journal.Entry
		if json.Unmarshal(it.Value(), &e) == nil && e.Kind == journal.KindFanOutLegStarted {
			n++
		}
	}
	if err := it.Err(); err != nil {
		t.Fatalf("scan raw journal: %v", err)
	}
	return n
}

func TestConcurrentAdaptersShareAttemptCap(t *testing.T) {
	_, js, raw := newAdapter(t)
	ctx := context.Background()
	b := &slotBarrier{Store: raw, t: t, ready: make(chan struct{})}
	a1, _ := newLegAdapter(js, b)
	a2, _ := newLegAdapter(js, b)
	var mu sync.Mutex
	var wg sync.WaitGroup
	var got []uint64
	refused := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(a *journalAppenderAdapter) {
			defer wg.Done()
			n, err := a.AppendLeg(ctx, "fanout_leg_started", "fo-cc", 0, map[string]string{"request_digest": "d"})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				got = append(got, n)
			case err == ErrLegAttemptsExhausted && err.Error() == ErrLegAttemptsExhausted.Error():
				refused++
			default:
				t.Errorf("start: %v", err)
			}
		}([]*journalAppenderAdapter{a1, a2}[i%2])
	}
	wg.Wait()
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	if fmt.Sprint(got) != "[1 2 3]" || refused != 5 || rawStarts(t, raw, "fo-cc") != 3 {
		t.Fatalf("attempts %v, refused %d, raw starts %d; want [1 2 3], 5 and 3", got, refused, rawStarts(t, raw, "fo-cc"))
	}
	restarted, _ := newLegAdapter(journal.New(raw, testkit.NewFrozenClock(testInstant), journal.DefaultNamespace), raw)
	if _, err := restarted.AppendLeg(ctx, "fanout_leg_started", "fo-cc", 0, nil); err != ErrLegAttemptsExhausted {
		t.Fatalf("fourth raw start after a restart = %v, want ErrLegAttemptsExhausted", err)
	}
	if n, err := restarted.AppendLeg(ctx, "fanout_leg_started", "fo-cc", 1, nil); err != nil || n != 1 || rawStarts(t, raw, "fo-cc") != 4 {
		t.Fatalf("other leg's first start = (%d, %v), want 1 and four raw starts in all", n, err)
	}
}

// flakyStore fails the next n Tx calls with a KindConflict that writes
// nothing, as a lock or busy conflict would, leaving the slot absent.
type flakyStore struct {
	provider.Store
	n atomic.Int32
}

var errTransient = cascade.New(cascade.KindConflict, "test: transient store conflict")

func (f *flakyStore) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	if f.n.Add(-1) >= 0 {
		return errTransient
	}
	return f.Store.Tx(ctx, fn)
}

// attemptSlots lists fo-tc leg 0's stored attempt slots.
func attemptSlots(t *testing.T, raw provider.Store) string {
	t.Helper()
	var keys []string
	for n := 1; n <= maxLegStarts; n++ {
		key := conductor.LegResultKey("fo-tc", 0) + "#" + itoa(uint64(n))
		if _, err := raw.Get(context.Background(), legAttemptsNamespace, key); err == nil {
			keys = append(keys, key)
		} else if !cascade.HasKind(err, cascade.KindNotFound) {
			t.Fatalf("read slot %s: %v", key, err)
		}
	}
	return fmt.Sprint(keys)
}

func TestClaimAttemptTransientConflictKeepsSlot(t *testing.T) {
	ctx := context.Background()
	start := func(taken bool, conflicts int32) (*flakyStore, provider.Store, uint64, error) {
		_, js, raw := newAdapter(t)
		if taken {
			if err := raw.Put(ctx, legAttemptsNamespace, conductor.LegResultKey("fo-tc", 0)+"#1", []byte("{}")); err != nil {
				t.Fatalf("seed slot 1: %v", err)
			}
		}
		f := &flakyStore{Store: raw}
		f.n.Store(conflicts)
		a, _ := newLegAdapter(js, f)
		n, err := a.AppendLeg(ctx, "fanout_leg_started", "fo-tc", 0, map[string]string{"request_digest": "d"})
		return f, raw, n, err
	}
	t.Run("one transient conflict keeps slot 1", func(t *testing.T) {
		if _, raw, n, err := start(false, 1); err != nil || n != 1 || attemptSlots(t, raw) != "[fo-tc#0#1]" || rawStarts(t, raw, "fo-tc") != 1 {
			t.Fatalf("after one transient conflict: attempt (%d, %v), slots %s, raw starts %d; want 1, [fo-tc#0#1], 1", n, err, attemptSlots(t, raw), rawStarts(t, raw, "fo-tc"))
		}
	})
	t.Run("persistent conflicts fail closed with the store error", func(t *testing.T) {
		_, raw, n, err := start(false, 1000)
		if err == nil || err == ErrLegAttemptsExhausted || err.Error() == ErrLegAttemptsExhausted.Error() ||
			!strings.Contains(err.Error(), errTransient.Error()) || n != 0 {
			t.Fatalf("persistent transient conflicts = (%d, %v), want the store error, not ErrLegAttemptsExhausted", n, err)
		}
		if attemptSlots(t, raw) != "[]" || rawStarts(t, raw, "fo-tc") != 0 {
			t.Fatalf("persistent conflicts stored slots %s and %d raw starts, want none", attemptSlots(t, raw), rawStarts(t, raw, "fo-tc"))
		}
	})
	t.Run("a taken slot advances past a transient conflict on the next", func(t *testing.T) {
		if _, raw, n, err := start(true, 2); err != nil || n != 2 || attemptSlots(t, raw) != "[fo-tc#0#1 fo-tc#0#2]" || rawStarts(t, raw, "fo-tc") != 1 {
			t.Fatalf("taken slot 1: attempt (%d, %v), slots %s, raw starts %d; want 2, both slots, 1", n, err, attemptSlots(t, raw), rawStarts(t, raw, "fo-tc"))
		}
	})
}

// TestScanEntityRefusesFanOutCursorInWrongEntity seeds a well-formed
// fan-out cursor under a plain task entity next to a correctly placed one.
// scanEntity must refuse the misplaced one with ErrUnrecognizedShape (by
// identity and message: errors.Is compares Kind only); Run reports it
// terminal and the correctly placed one resumable, and dispatches neither
// (classify-only: no entity gains an entry).
func TestScanEntityRefusesFanOutCursorInWrongEntity(t *testing.T) {
	store, _, _ := newRealStore(t)
	ctx := context.Background()
	cursor := cursorPayloadFor(t, "t-misplaced", "t-misplaced", 1)
	if _, err := store.Append(ctx, "t-misplaced", journal.KindResumeCursor, "cursor-op", cursor); err != nil {
		t.Fatalf("seed misplaced cursor: %v", err)
	}
	seedFanOutCursor(t, store, "t-placed", 1)

	mgr, err := New(store, nil, nil, "darwin")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cur, attention, err := mgr.scanEntity(ctx, "t-misplaced")
	if cur != nil || attention != nil || err != ErrUnrecognizedShape || err.Error() != ErrUnrecognizedShape.Error() {
		t.Fatalf("scanEntity(misplaced) = (%+v, %+v, %v), want (nil, nil, ErrUnrecognizedShape)", cur, attention, err)
	}

	report, err := mgr.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	byEntity := map[string]Outcome{}
	for _, o := range report.Outcomes {
		byEntity[o.EntityID] = o
	}
	bad := byEntity["t-misplaced"]
	if len(report.Outcomes) != 2 || bad.Classification != ClassTerminal || bad.Err != ErrUnrecognizedShape {
		t.Fatalf("Outcomes = %+v, want the misplaced entity terminal with ErrUnrecognizedShape", report.Outcomes)
	}
	good := byEntity[FanOutEntity("t-placed")]
	if good.Classification != ClassResumable || good.Err != nil {
		t.Fatalf("placed fan-out outcome = %+v, want resumable", good)
	}
	if a, b := entryCount(t, store, "t-misplaced"), entryCount(t, store, FanOutEntity("t-placed")); a != 1 || b != 1 {
		t.Fatalf("entries after Run: misplaced %d, placed %d; want 1 and 1 (nothing dispatched or appended)", a, b)
	}
}
