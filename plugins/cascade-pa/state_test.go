package cascadepa

// Purpose (this file): the durable-state seam's own tests — the bounded replay
//   window, the offset that advances with it, and the fail-closed default.
//
// SPORT: plugins/cascade-pa state-tests/TEST (P1-E23-W5-S48-T1).

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestSubjectState_AllowlistHelpers(t *testing.T) {
	var st SubjectState
	if st.Bound() {
		t.Fatal("an empty state reported itself bound")
	}
	st.admit("a")
	st.admit("a")
	st.admit("b")
	if len(st.AllowedFrom) != 2 {
		t.Fatalf("allowlist = %v, want two entries with no duplicate", st.AllowedFrom)
	}
	if !st.Allows("a") || !st.Allows("b") || st.Allows("c") {
		t.Fatalf("Allows is wrong for %v", st.AllowedFrom)
	}
	if !st.Bound() {
		t.Fatal("a state with an allowlist reported itself unbound")
	}
}

// TestSubjectState_ReplayWindowIsBounded: the window must not grow forever in a
// row nothing prunes, and the NEWEST entries are the ones kept.
func TestSubjectState_ReplayWindowIsBounded(t *testing.T) {
	var st SubjectState
	for i := 0; i < MaxSeenUpdateIDs+50; i++ {
		st.remember(int64(i))
	}
	if len(st.SeenUpdateIDs) != MaxSeenUpdateIDs {
		t.Fatalf("window holds %d ids, want the %d cap", len(st.SeenUpdateIDs), MaxSeenUpdateIDs)
	}
	if !st.seen(int64(MaxSeenUpdateIDs + 49)) {
		t.Fatal("the newest id was trimmed")
	}
	if st.seen(0) {
		t.Fatal("the oldest id survived the trim")
	}
}

func TestUpdateLedger_AcceptIsIdempotentAndAdvancesTheOffset(t *testing.T) {
	ctx := context.Background()
	state := newMemState()
	ledger := NewUpdateLedger(state)

	if offset, err := ledger.Offset(ctx, testSubject); err != nil || offset != 0 {
		t.Fatalf("initial Offset = (%d, %v), want (0, nil)", offset, err)
	}
	fresh, err := ledger.Accept(ctx, testSubject, 900000001)
	if err != nil || !fresh {
		t.Fatalf("first Accept = (%v, %v)", fresh, err)
	}
	if offset, _ := ledger.Offset(ctx, testSubject); offset != 900000002 {
		t.Fatalf("Offset = %d, want 900000002", offset)
	}
	again, err := ledger.Accept(ctx, testSubject, 900000001)
	if err != nil {
		t.Fatalf("replayed Accept: %v", err)
	}
	if again {
		t.Fatal("a replayed update id was reported as new")
	}
	// An OLDER id (a proxy replaying an earlier batch) must not rewind the
	// offset: rewinding refetches everything after it, forever.
	if _, err := ledger.Accept(ctx, testSubject, 900000000); err != nil {
		t.Fatalf("older Accept: %v", err)
	}
	if offset, _ := ledger.Offset(ctx, testSubject); offset != 900000002 {
		t.Fatalf("Offset rewound to %d", offset)
	}
}

func TestUpdateLedger_SurvivesRestart(t *testing.T) {
	ctx := context.Background()
	state := newMemState()
	if fresh, err := NewUpdateLedger(state).Accept(ctx, testSubject, 42); err != nil || !fresh {
		t.Fatalf("Accept = (%v, %v)", fresh, err)
	}
	restarted := NewUpdateLedger(state)
	if fresh, err := restarted.Accept(ctx, testSubject, 42); err != nil || fresh {
		t.Fatalf("the restarted ledger reported id 42 as new: (%v, %v)", fresh, err)
	}
	if offset, _ := restarted.Offset(ctx, testSubject); offset != 43 {
		t.Fatalf("restarted Offset = %d, want 43", offset)
	}
}

// TestUpdateLedger_NoStateStoreRefuses: the fail-closed default. An unwired
// ledger cannot say whether an id is new, so it says nothing usable.
func TestUpdateLedger_NoStateStoreRefuses(t *testing.T) {
	ctx := context.Background()
	ledger := NewUpdateLedger(nil)
	if _, err := ledger.Offset(ctx, testSubject); err == nil {
		t.Fatal("Offset succeeded with no state store")
	}
	_, err := ledger.Accept(ctx, testSubject, 1)
	if err == nil {
		t.Fatal("Accept succeeded with no state store")
	}
	if !strings.Contains(err.Error(), ErrNoBridgeState.Error()) {
		t.Fatalf("got %v, want ErrNoBridgeState", err)
	}
}

func TestUpdateLedger_StoreFailuresPropagate(t *testing.T) {
	ctx := context.Background()
	state := newMemState()
	state.saveErr = errStoreDown
	if _, err := NewUpdateLedger(state).Accept(ctx, testSubject, 1); err == nil {
		t.Fatal("Accept reported a decision although the write failed")
	}
	state.saveErr, state.loadErr = nil, errStoreDown
	if _, err := NewUpdateLedger(state).Accept(ctx, testSubject, 1); err == nil {
		t.Fatal("Accept reported a decision although the read failed")
	}
}

// TestUpdateLedger_ConcurrentAcceptAdmitsOneWinner: two goroutines racing on
// one update id must produce exactly one "new". The assertion counts winners
// rather than merely not deadlocking, so a ledger without its lock fails here
// instead of passing by arriving late.
func TestUpdateLedger_ConcurrentAcceptAdmitsOneWinner(t *testing.T) {
	ctx := context.Background()
	ledger := NewUpdateLedger(newMemState())
	const racers = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners int
		start   = make(chan struct{})
	)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			fresh, err := ledger.Accept(ctx, testSubject, 7)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("Accept: %v", err)
				return
			}
			if fresh {
				winners++
			}
		}()
	}
	close(start)
	wg.Wait()
	if winners != 1 {
		t.Fatalf("%d goroutines saw update 7 as new, want exactly 1", winners)
	}
}

func TestUnconfiguredBridgeState_RefusesBothVerbs(t *testing.T) {
	ctx := context.Background()
	var state BridgeState = unconfiguredBridgeState{}
	if _, _, err := state.Load(ctx, testSubject); err == nil {
		t.Fatal("Load succeeded")
	}
	if err := state.Save(ctx, SubjectState{Subject: testSubject}); err == nil {
		t.Fatal("Save succeeded")
	}
}

func TestOrUnconfigured(t *testing.T) {
	store := newMemState()
	if got := orUnconfigured(store); got != BridgeState(store) {
		t.Fatal("orUnconfigured replaced a real store")
	}
	if _, ok := orUnconfigured(nil).(unconfiguredBridgeState); !ok {
		t.Fatal("orUnconfigured(nil) did not resolve to the fail-closed default")
	}
}

// TestPairCodeStore_NoStateStoreRefusesEveryVerb is the fail-closed default: a
// store with no durable state issues nothing and verifies nothing.
func TestPairCodeStore_NoStateStoreRefusesEveryVerb(t *testing.T) {
	ctx := context.Background()
	store := NewPairCodeStore(fixedTestClock{t0()}, nil, testPairKey(t))
	if _, err := store.IssueCode(ctx, fixedEntropy(), testSubject); err == nil {
		t.Fatal("IssueCode succeeded with no state store")
	}
	if _, err := store.VerifyAndConsume(ctx, testSubject, "ABCDEFGH"); err == nil {
		t.Fatal("VerifyAndConsume succeeded with no state store")
	}
	if _, err := store.Pending(ctx, testSubject); err == nil {
		t.Fatal("Pending succeeded with no state store")
	}
}

// TestPairCodeStore_StoreFailuresPropagate: a write that did not land must not
// be reported as a pairing decision.
func TestPairCodeStore_StoreFailuresPropagate(t *testing.T) {
	ctx := context.Background()
	state := newMemState()
	store := NewPairCodeStore(fixedTestClock{t0()}, state, testPairKey(t))
	code, err := store.IssueCode(ctx, fixedEntropy(), testSubject)
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}

	state.saveErr = errStoreDown
	if _, err := store.VerifyAndConsume(ctx, testSubject, code); err == nil {
		t.Fatal("a bind whose write failed was reported as a decision")
	}
	if _, err := store.VerifyAndConsume(ctx, testSubject, "ZZZZZZZZ"); err == nil {
		t.Fatal("a refusal whose attempt-counter write failed was reported as a decision")
	}
	state.saveErr, state.loadErr = nil, errStoreDown
	if _, err := store.IssueCode(ctx, fixedEntropy(), testSubject); err == nil {
		t.Fatal("IssueCode succeeded against an unreadable store")
	}
	if _, err := store.VerifyAndConsume(ctx, testSubject, code); err == nil {
		t.Fatal("VerifyAndConsume succeeded against an unreadable store")
	}
	if _, err := store.Pending(ctx, testSubject); err == nil {
		t.Fatal("Pending succeeded against an unreadable store")
	}
}

// TestIssueCode_SaveFailurePropagates covers the issuance write path's own
// failure branch.
func TestIssueCode_SaveFailurePropagates(t *testing.T) {
	state := newMemState()
	state.saveErr = errStoreDown
	store := NewPairCodeStore(fixedTestClock{t0()}, state, testPairKey(t))
	if _, err := store.IssueCode(context.Background(), fixedEntropy(), testSubject); err == nil {
		t.Fatal("IssueCode succeeded when the write failed")
	}
}
