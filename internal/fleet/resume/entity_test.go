// Purpose: FanOutEntity and its inverse round-trip, and the inverse
//   refuses every non-fan-out entity id; per-leg attempts of one fan-out
//   entity stay unique and capped across adapters sharing one store.
// SPORT: internal.fleet.resume.ResumeManager/CHANGE (tests) (P1-CORE-18).

package resume

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/testkit"
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

// startBarrier holds the first two start-count reads of a leg until both
// have read, forcing the interleaving where a per-adapter lock lets two
// adapters see the same count. An allocation that never counts through
// Replay never waits here.
type startBarrier struct {
	journal.Store
	reads atomic.Int32
	ready chan struct{}
}

func (b *startBarrier) Replay(ctx context.Context, id string, c journal.Cursor, k []journal.Kind) ([]journal.Entry, error) {
	entries, err := b.Store.Replay(ctx, id, c, k)
	if len(k) == 1 && k[0] == journal.KindFanOutLegStarted {
		if n := b.reads.Add(1); n == 2 {
			close(b.ready)
		} else if n < 2 {
			<-b.ready
		}
	}
	return entries, err
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
	b := &startBarrier{Store: js, ready: make(chan struct{})}
	a1, _ := newLegAdapter(b, raw)
	a2, _ := newLegAdapter(b, raw)
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
