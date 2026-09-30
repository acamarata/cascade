package governor

// Purpose: EscalationLadder's per-entity lock map stays bounded (entries
//
//	are evicted once no holder or waiter remains) without losing the
//	per-entity serialization Advance relies on. Run with -race.
import (
	"context"
	"fmt"
	goruntime "runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

func ladderLockEntries(l *EscalationLadder) int {
	l.entityLocks.mu.Lock()
	defer l.entityLocks.mu.Unlock()
	return len(l.entityLocks.m)
}

func TestEscalationLadderLocksEvicted(t *testing.T) {
	store := newFakeJournalStore()
	l := newTestEscalationLadder(store, fixedConfidence{v: 0}, newCountingSeams(), testPolicy(), runtime.NewFixedClock(time.Unix(0, 0)))
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("entity-%d", i)
		if err := l.Advance(context.Background(), id); err != nil {
			t.Fatalf("Advance(%s) = %v", id, err)
		}
		if store.count(id) != 1 {
			t.Fatalf("Advance(%s) journaled %d events, want 1", id, store.count(id))
		}
	}
	if n := ladderLockEntries(l); n != 0 {
		t.Fatalf("lock map holds %d entries after 1000 finished Advance calls, want 0", n)
	}

	if overlaps := hammerOneEntity(l.entityLocks.acquire); overlaps != 0 {
		t.Fatalf("critical-section overlaps on one entity = %d, want 0", overlaps)
	}
	if n := ladderLockEntries(l); n != 0 {
		t.Fatalf("lock map holds %d entries after the barrier run, want 0", n)
	}
}

// hammerOneEntity releases 16 goroutines at once through a barrier, each
// taking the same entity's lock 200 times, and returns how many times a
// goroutine found another already inside the critical section.
func hammerOneEntity(acquire func(string) func()) int64 {
	const workers, rounds = 16, 200
	var inside, overlaps atomic.Int64
	var ready, done sync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < workers; w++ {
		ready.Add(1)
		done.Add(1)
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			for r := 0; r < rounds; r++ {
				release := acquire("one-entity")
				if inside.Add(1) != 1 {
					overlaps.Add(1)
				}
				goruntime.Gosched()
				inside.Add(-1)
				release()
			}
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	return overlaps.Load()
}
