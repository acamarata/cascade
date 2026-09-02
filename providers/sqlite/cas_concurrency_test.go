// Purpose: CR nit 2 — probes CompareAndSwap under genuine goroutine
//
//	concurrency rather than resting solely on the CR's code-reading
//	argument ("CAS is atomic by construction because the executor runs
//	jobs one at a time on a single goroutine"). Many goroutines race a
//	read-then-CAS increment loop against the SAME key; the assertion is
//	the strongest one available — the final counter value must equal
//	exactly the goroutine count, which is only possible if every
//	increment took effect exactly once (no lost update) and no CAS ever
//	incorrectly succeeded against a stale value (which would either skip
//	or double-count an increment). Run under -race per the ticket.
//
// SPORT: providers.sqlite.Driver/CHANGED (P1-E02-W1-S02-T2 CR fix).
package sqlite_test

import (
	"context"
	"strconv"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestDriver_CompareAndSwap_ConcurrentNoLostUpdate spawns goroutines that
// each read the current counter value OUTSIDE any transaction, then race
// to CompareAndSwap their own observed old value to old+1 inside a Tx. A
// goroutine that loses the race (its old value is stale by the time its
// CAS runs) sees KindConflict and retries against the new value — exactly
// one goroutine can win each round, since a real CAS lets only the
// caller whose "old" matches the currently-stored value succeed.
func TestDriver_CompareAndSwap_ConcurrentNoLostUpdate(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	const namespace, key = "cas", "counter"
	const goroutines = 32

	if err := d.Put(ctx, namespace, key, []byte("0")); err != nil {
		t.Fatalf("seed Put: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			casIncrementUntilWin(ctx, d, namespace, key, errs)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("unexpected CAS error: %v", err)
	}

	got, err := d.Get(ctx, namespace, key)
	if err != nil {
		t.Fatalf("final Get: %v", err)
	}
	if string(got) != strconv.Itoa(goroutines) {
		t.Fatalf("final counter = %q, want %d — a lost update or a double-applied CAS would leave this wrong", got, goroutines)
	}
}

// casIncrementUntilWin repeatedly reads the counter and races a
// CompareAndSwap increment against it until this goroutine's own attempt
// succeeds (KindConflict means another goroutine won that round — retry
// against the value it left behind). Any other error is reported on errs
// and the goroutine gives up.
func casIncrementUntilWin(ctx context.Context, d provider.Store, namespace, key string, errs chan<- error) {
	for {
		current, err := d.Get(ctx, namespace, key)
		if err != nil {
			errs <- err
			return
		}
		n, convErr := strconv.Atoi(string(current))
		if convErr != nil {
			errs <- convErr
			return
		}
		next := []byte(strconv.Itoa(n + 1))

		casErr := d.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
			return tx.CompareAndSwap(ctx, namespace, key, current, next)
		})
		if casErr == nil {
			return // this goroutine's increment won this round
		}
		if !cascade.HasKind(casErr, cascade.KindConflict) {
			errs <- casErr
			return
		}
		// lost the race — loop and retry against whatever the winner left behind
	}
}
