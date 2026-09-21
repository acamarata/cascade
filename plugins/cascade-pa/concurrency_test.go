package cascadepa

// Purpose (this file): the concurrency properties the bridge's stores have to
//
//	hold, and the keyed-digest refusals. Every test here failed on the draft
//	that had no per-subject lock and an unconditional upsert: two goroutines
//	both redeemed one code, and a Bind racing the poll loop lost its own
//	allowlist.
//
// Constraints: run under -race in the project's default lane. The in-memory
//
//	fake refuses a stale write exactly as internal/bridge's real store does
//	(bridge_helpers_test.go), so these are properties of the store code here,
//	not of the fake being permissive.
//
// SPORT: plugins/cascade-pa subjectLocks/TESTED, PairCodeStore/TESTED
//
//	(single-use under concurrency) — P1-E23-W5-S48-T1.

import (
	"context"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestVerifyAndConsume_IsSingleUseUnderTwoGoroutines: ONE code, two concurrent
// redeemers, exactly one Bound. Without the per-subject lock both observed the
// code outstanding and both bound.
func TestVerifyAndConsume_IsSingleUseUnderTwoGoroutines(t *testing.T) {
	ctx := context.Background()
	state := newMemState()
	store := NewPairCodeStore(fixedTestClock{t0()}, state, testPairKey(t))
	code, err := store.IssueCode(ctx, fixedEntropy(), testSubject)
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		bound    int
		outcomes []PairOutcome
	)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, verr := store.VerifyAndConsume(ctx, testSubject, code)
			mu.Lock()
			defer mu.Unlock()
			if verr != nil {
				t.Errorf("VerifyAndConsume: %v", verr)
				return
			}
			outcomes = append(outcomes, out)
			if out.Bound {
				bound++
			}
		}()
	}
	wg.Wait()
	if bound != 1 {
		t.Fatalf("%d of 2 concurrent redemptions bound one code (outcomes %+v); a one-use code was used twice",
			bound, outcomes)
	}
}

// TestRefusals_DoNotLoseAttemptsUnderConcurrency: five concurrent WRONG
// candidates must still reach the lockout, because the counter is what R-16.37's
// ceiling is made of. A lost increment is a code that never burns.
func TestRefusals_DoNotLoseAttemptsUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	state := newMemState()
	store := NewPairCodeStore(fixedTestClock{t0()}, state, testPairKey(t))
	if _, err := store.IssueCode(ctx, fixedEntropy(), testSubject); err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	locked := 0
	for range PairCodeMaxAttempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := store.VerifyAndConsume(ctx, testSubject, "00000000")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("VerifyAndConsume: %v", err)
				return
			}
			if out.LockedOut {
				locked++
			}
		}()
	}
	wg.Wait()
	if locked != 1 {
		t.Fatalf("%d attempts reported the lockout, want exactly 1 (an increment was lost)", locked)
	}
	row, _, err := state.Load(ctx, testSubject)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if row.WrongAttempts != PairCodeMaxAttempts || row.CodeDigest != "" {
		t.Fatalf("after %d wrong candidates: attempts=%d digest=%q; the code should be burned",
			PairCodeMaxAttempts, row.WrongAttempts, row.CodeDigest)
	}
}

// TestIssueCodeRacingAcceptLosesNothing is the interleaving that really happens
// in one daemon process: pa.pair_code issues a code on the RPC goroutine while
// the poll goroutine records an update. Neither may revert the other's columns.
func TestIssueCodeRacingAcceptLosesNothing(t *testing.T) {
	ctx := context.Background()
	state := newMemState()
	stores := NewStores(fixedTestClock{t0()}, &recordingRegistrar{}, state, testPairKey(t))
	if _, err := stores.Binding.Bind(ctx, testSubject, "111"); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := stores.Pairing.IssueCode(ctx, fixedEntropy(), testSubject); err != nil {
			t.Errorf("IssueCode: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if _, err := stores.Updates.Accept(ctx, testSubject, 900000001); err != nil {
			t.Errorf("Accept: %v", err)
		}
	}()
	wg.Wait()

	row, ok, err := state.Load(ctx, testSubject)
	if err != nil || !ok {
		t.Fatalf("Load = (%v, %v)", ok, err)
	}
	if row.Offset != 900000002 || !row.seen(900000001) {
		t.Fatalf("the poll ledger was reverted: offset=%d seen=%v", row.Offset, row.SeenUpdateIDs)
	}
	if row.CodeDigest == "" {
		t.Fatal("the issued code was reverted: no digest is outstanding")
	}
	if len(row.AllowedFrom) != 1 || row.AllowedFrom[0] != "111" {
		t.Fatalf("the binding was reverted (a bound bot silently unpaired): %v", row.AllowedFrom)
	}
}

// TestBindRacingAcceptKeepsBothWrites is the same property around the column
// whose loss is worst: AllowedFrom. An empty allowlist reads as "unpaired".
func TestBindRacingAcceptKeepsBothWrites(t *testing.T) {
	ctx := context.Background()
	state := newMemState()
	stores := NewStores(fixedTestClock{t0()}, &recordingRegistrar{}, state, testPairKey(t))
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := stores.Binding.Bind(ctx, testSubject, "222"); err != nil {
			t.Errorf("Bind: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if _, err := stores.Updates.Accept(ctx, testSubject, 7); err != nil {
			t.Errorf("Accept: %v", err)
		}
	}()
	wg.Wait()
	allowed, err := stores.Binding.IsAllowed(ctx, testSubject, "222")
	if err != nil {
		t.Fatalf("IsAllowed: %v", err)
	}
	if !allowed {
		t.Fatal("the binding was lost to a concurrent ledger write")
	}
	offset, err := stores.Updates.Offset(ctx, testSubject)
	if err != nil {
		t.Fatalf("Offset: %v", err)
	}
	if offset != 8 {
		t.Fatalf("offset = %d, want 8; the ledger write was lost to the bind", offset)
	}
}

// TestIsStateConflict_DoesNotMatchEveryConflict: the predicate has to be
// narrower than its kind, or the retry would fire on unrelated failures.
func TestIsStateConflict_DoesNotMatchEveryConflict(t *testing.T) {
	if IsStateConflict(nil) {
		t.Fatal("nil is a conflict")
	}
	if IsStateConflict(cascade.New(cascade.KindConflict, "nodes: device already enrolled")) {
		t.Fatal("an unrelated KindConflict was read as a compare-and-swap refusal")
	}
	if !IsStateConflict(ErrStateConflict) {
		t.Fatal("the package's own sentinel is not recognised")
	}
}

// permissiveState is a BridgeState that IGNORES Version — a host store with no
// compare-and-swap at all. It exists so the per-subject LOCK can be tested on
// its own: with the store's CAS doing none of the work, a single-use code that
// is still used once can only be the lock's doing.
type permissiveState struct {
	mu   sync.Mutex
	rows map[string]SubjectState
}

func newPermissiveState() *permissiveState {
	return &permissiveState{rows: map[string]SubjectState{}}
}

func (p *permissiveState) Load(_ context.Context, subject string) (SubjectState, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st, ok := p.rows[subject]
	return st, ok, nil
}

func (p *permissiveState) Save(_ context.Context, st SubjectState) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	st.Version++
	p.rows[st.Subject] = st
	return nil
}

func TestVerifyAndConsume_IsSingleUseEvenWithoutStoreCAS(t *testing.T) {
	ctx := context.Background()
	store := NewPairCodeStore(fixedTestClock{t0()}, newPermissiveState(), testPairKey(t))
	code, err := store.IssueCode(ctx, fixedEntropy(), testSubject)
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		bound int
	)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, verr := store.VerifyAndConsume(ctx, testSubject, code)
			mu.Lock()
			defer mu.Unlock()
			if verr != nil {
				t.Errorf("VerifyAndConsume: %v", verr)
				return
			}
			if out.Bound {
				bound++
			}
		}()
	}
	wg.Wait()
	if bound != 1 {
		t.Fatalf("%d of 4 redemptions bound one code with no store-side CAS; the per-subject lock is not serialising",
			bound)
	}
}
