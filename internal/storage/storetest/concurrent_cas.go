// Purpose: exercise competing conditional creates and swaps through Store.Tx.
// Inputs: a factory producing an isolated store per case.
// Outputs: failures unless one of sixteen contenders commits and owns the value.
// Constraints: start outside Tx so stores with serialized transactions cannot deadlock.
// SPORT: internal.storage.storetest/CHANGED (P1-SEC-37).

package storetest

import (
	"context"
	"fmt"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// RunConcurrentCAS requires exactly one committed winner and fifteen typed
// conflicts for both absent-key creation and replacement of the same old value.
func RunConcurrentCAS(t *testing.T, open func(t *testing.T) provider.Store) {
	t.Helper()
	t.Run("ConcurrentCreateOneWins", func(t *testing.T) { runCASRace(t, open(t), nil) })
	t.Run("ConcurrentSwapOneWins", func(t *testing.T) { runCASRace(t, open(t), []byte("old")) })
}

type casResult struct {
	value []byte
	err   error
}

// runCASRace validates committed outcomes and the winning bytes, not callback success.
func runCASRace(t *testing.T, s provider.Store, old []byte) {
	t.Helper()
	ctx := testContext(t)
	namespace := t.Name()
	requireNoError(t, s.Delete(ctx, namespace, "cas"), "reset CAS key")
	if old != nil {
		requireNoError(t, s.Put(ctx, namespace, "cas", old), "seed CAS key")
	}
	start := make(chan struct{})
	results := make(chan casResult, 16)
	for i := range 16 {
		go func() {
			value := []byte(fmt.Sprintf("winner-%d", i))
			<-start
			err := s.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
				return tx.CompareAndSwap(ctx, namespace, "cas", old, value)
			})
			results <- casResult{value: value, err: err}
		}()
	}
	close(start)
	winners := 0
	var winner []byte
	for range 16 {
		select {
		case result := <-results:
			if result.err == nil {
				winners++
				winner = result.value
			} else {
				requireErrorKind(t, result.err, cascade.KindConflict, "losing CAS")
			}
		case <-ctx.Done():
			t.Fatal("concurrent CAS did not finish:", ctx.Err())
		}
	}
	if winners != 1 {
		t.Fatalf("CAS winners = %d, want exactly 1 of 16", winners)
	}
	got, err := s.Get(ctx, namespace, "cas")
	requireNoError(t, err, "Get after concurrent CAS")
	requireBytesEqual(t, got, winner, "committed CAS winner")
}
