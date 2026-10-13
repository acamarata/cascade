//go:build postgres

package audit

// Purpose: the postgres half of the chain-safety acceptance. Two Log values
//   over one postgres store append concurrently, the way two writers in
//   separate processes would; the head compare-and-swap and the conditional
//   record create must keep the chain single and complete.
// Inputs: CASCADE_TEST_POSTGRES_DSN, a real reachable PostgreSQL server
//   (openPostgresEffectStore skips loudly without one).
// Constraints: stored state is asserted, never events; the namespace is
//   emptied before the test by openPostgresEffectStore.
// SPORT: internal.audit.Log/ADDED (tests) (P1-SEC-06).

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// appendBudget bounds the caller-level retry around an append that spent its
// own three retries losing to the other writer. Two writers hammering one
// head row from tight loops can starve one another until the other finishes
// (the loser is queued behind the winner's commit on the row lock, and its
// compare value is stale by the time it gets the lock); Append reports that
// as KindConflict, which is a liveness limit, never a fork, and a caller
// tries again. The multi-process test does the same around its opens.
const appendBudget = 60 * time.Second

func TestAuditTwoLogsPostgresChainVerifies(t *testing.T) {
	ctx := context.Background()
	store := openPostgresEffectStore(t)
	clock := testkit.NewFrozenClock(testInstant)
	logs := []*Log{New(store, clock, nil), New(store, clock, nil)}

	const each = 50
	var wg sync.WaitGroup
	for w, log := range logs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if err := appendPastConflicts(ctx, log, sampleEvent(w*each+i)); err != nil {
					t.Errorf("writer %d append %d: %v", w, i, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if t.Failed() {
		return
	}

	if err := logs[0].Verify(ctx); err != nil {
		t.Fatalf("Verify after concurrent appends over postgres: %v", err)
	}
	records := scanNamespace(t, store, recordPrefix)
	if len(records) != 2*each {
		t.Fatalf("store holds %d records, want %d", len(records), 2*each)
	}
	seen := map[uint64]bool{}
	for _, raw := range records {
		rec, err := decodeRecord(raw)
		if err != nil {
			t.Fatalf("decoding a stored record: %v", err)
		}
		if seen[rec.Seq] {
			t.Fatalf("sequence %d appears twice", rec.Seq)
		}
		seen[rec.Seq] = true
	}
}

// appendPastConflicts appends, trying again while the Log's own retries lose
// every race and the budget lasts; any other failure is returned at once.
func appendPastConflicts(ctx context.Context, log *Log, event Event) error {
	deadline := time.Now().Add(appendBudget)
	for {
		_, err := log.Append(ctx, event)
		if err == nil || !cascade.HasKind(err, cascade.KindConflict) || time.Now().After(deadline) {
			return err
		}
	}
}
