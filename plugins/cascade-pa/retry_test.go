package cascadepa

// Purpose (this file): the compare-and-swap RETRY — that a refused write is
//   re-read and completed once, and that a conflict which survives that retry
//   reaches the caller instead of being spun on. Split from concurrency_test.go
//   for Art.10.3's 300-line cap.
//
// SPORT: plugins/cascade-pa mutate/TESTED (P1-E23-W5-S48-T1).

import (
	"context"
	"sync"
	"testing"
)

// conflictOnce refuses the FIRST Save with a compare-and-swap conflict and then
// delegates — a second writer that got there first, exactly once. It proves the
// retry re-reads and completes rather than surfacing the conflict to the caller.
type conflictOnce struct {
	BridgeState
	mu    sync.Mutex
	fired bool
}

func (c *conflictOnce) Save(ctx context.Context, st SubjectState) error {
	c.mu.Lock()
	first := !c.fired
	c.fired = true
	c.mu.Unlock()
	if first {
		return ErrStateConflict
	}
	return c.BridgeState.Save(ctx, st)
}

func TestMutate_RetriesOnceAfterAConflict(t *testing.T) {
	ctx := context.Background()
	inner := newMemState()
	state := &conflictOnce{BridgeState: inner}
	store := NewPairCodeStore(fixedTestClock{t0()}, state, testPairKey(t))
	code, err := store.IssueCode(ctx, fixedEntropy(), testSubject)
	if err != nil {
		t.Fatalf("IssueCode after one conflict: %v", err)
	}
	if !state.fired {
		t.Fatal("the conflict branch never ran, so nothing was retried")
	}
	row, ok, err := inner.Load(ctx, testSubject)
	if err != nil || !ok {
		t.Fatalf("Load = (%v, %v)", ok, err)
	}
	want, err := CodeDigest(testPairKey(t), code)
	if err != nil {
		t.Fatalf("CodeDigest: %v", err)
	}
	if row.CodeDigest != want {
		t.Fatal("the retry did not persist the issued code")
	}
}

// alwaysConflict refuses every write. A conflict that SURVIVES the single retry
// must reach the caller: a store under sustained contention is a signal, not
// something to spin on.
type alwaysConflict struct{ BridgeState }

func (alwaysConflict) Save(context.Context, SubjectState) error { return ErrStateConflict }

func TestMutate_SurfacesASecondConflict(t *testing.T) {
	store := NewPairCodeStore(fixedTestClock{t0()}, alwaysConflict{newMemState()}, testPairKey(t))
	_, err := store.IssueCode(context.Background(), fixedEntropy(), testSubject)
	if !IsStateConflict(err) {
		t.Fatalf("IssueCode = %v, want the conflict surfaced after the retry", err)
	}
}
