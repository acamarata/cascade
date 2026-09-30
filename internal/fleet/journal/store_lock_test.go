package journal

// Purpose: the store's per-entity lock map stays bounded (entries are
//
//	evicted once no holder or waiter remains) without losing the
//	per-entity serialization Append's seq allocation relies on. Run with
//	-race.
import (
	"context"
	"encoding/json"
	"fmt"
	goruntime "runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
)

func storeLockEntries(s *SQLiteStore) int {
	s.entityLocks.mu.Lock()
	defer s.entityLocks.mu.Unlock()
	return len(s.entityLocks.m)
}

func TestJournalStoreEntityLocksEvicted(t *testing.T) {
	s := New(newFakeStore(), testkit.NewFrozenClock(time.Unix(0, 0)), DefaultNamespace)
	ctx := context.Background()
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("entity-%d", i)
		e, err := s.Append(ctx, id, KindIntent, "op-"+id, json.RawMessage(`{}`))
		if err != nil || e.Seq != 1 {
			t.Fatalf("Append(%s) = seq %d, %v; want seq 1", id, e.Seq, err)
		}
	}
	if n := storeLockEntries(s); n != 0 {
		t.Fatalf("lock map holds %d entries after 1000 finished Appends, want 0", n)
	}

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
				lock := s.lockFor("one-entity")
				lock.Lock()
				if inside.Add(1) != 1 {
					overlaps.Add(1)
				}
				goruntime.Gosched()
				inside.Add(-1)
				lock.Unlock()
			}
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	if got := overlaps.Load(); got != 0 {
		t.Fatalf("critical-section overlaps on one entity = %d, want 0", got)
	}
	if n := storeLockEntries(s); n != 0 {
		t.Fatalf("lock map holds %d entries after the barrier run, want 0", n)
	}
}
