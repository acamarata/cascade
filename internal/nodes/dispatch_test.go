package nodes

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the dispatch admission and fencing rules — what may
//   be shipped where, and how a superseded attempt is kept from racing its
//   replacement.
// SPORT: internal/nodes tests (ADD) — P1-E17-W4-S37-T2.

// TestLocalOnlyWorkIsNeverDispatched is the rule that cannot have an
// exception. It is checked against EVERY tier, including the highest, so
// the test cannot pass merely because the tier under test was too low.
func TestLocalOnlyWorkIsNeverDispatched(t *testing.T) {
	for _, tier := range []Tier{TierController, TierWorkerTrusted, TierPairedDevice} {
		err := AdmitDispatch(SensitivityLocalOnly, tier)
		if err == nil {
			t.Errorf("local-only work was admitted to a %q node", tier)
			continue
		}
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPolicyDenied {
			t.Errorf("tier %q: kind = %v (ok=%v), want KindPolicyDenied", tier, kind, ok)
		}
	}
}

// TestRestrictedWorkNeedsARestrictedGateTier pins the middle rule against
// every tier rather than one, so a change to the rank order fails here.
func TestRestrictedWorkNeedsARestrictedGateTier(t *testing.T) {
	for tier, wantAdmitted := range map[Tier]bool{
		TierController:    true,
		TierWorkerTrusted: true,
		TierPairedDevice:  false,
	} {
		err := AdmitDispatch(SensitivityRestricted, tier)
		if wantAdmitted && err != nil {
			t.Errorf("tier %q was refused restricted work: %v", tier, err)
		}
		if !wantAdmitted && err == nil {
			t.Errorf("tier %q was admitted restricted work", tier)
		}
	}
}

// TestUnresolvableTiersFailClosed is the fail-closed half. An unreadable
// tier on either side must refuse, never fall through to normal — a
// corrupted record or a value from a newer build would otherwise silently
// admit work the operator never granted.
func TestUnresolvableTiersFailClosed(t *testing.T) {
	if err := AdmitDispatch(SensitivityNormal, Tier("")); err == nil {
		t.Error("an empty node tier was admitted")
	}
	if err := AdmitDispatch(SensitivityNormal, Tier("supervisor")); err == nil {
		t.Error("an unrecognized node tier was admitted")
	}
	if err := AdmitDispatch(Sensitivity("secret-ish"), TierController); err == nil {
		t.Error("an unrecognized work sensitivity was admitted")
	}
	if err := AdmitDispatch(Sensitivity(""), TierController); err == nil {
		t.Error("an empty work sensitivity was admitted")
	}
}

// TestNormalWorkGoesToAnyKnownTier proves the rules above are not simply
// refusing everything — the assertion that keeps the others honest.
func TestNormalWorkGoesToAnyKnownTier(t *testing.T) {
	for _, tier := range []Tier{TierController, TierWorkerTrusted, TierPairedDevice} {
		if err := AdmitDispatch(SensitivityNormal, tier); err != nil {
			t.Errorf("normal work was refused a %q node: %v", tier, err)
		}
	}
}

// TestAttemptsAreMonotonicPerDispatch pins the fencing counter, including
// that two dispatches do not share one.
func TestAttemptsAreMonotonicPerDispatch(t *testing.T) {
	reg := NewAttemptRegister()

	if got := reg.Current("d1"); got != 0 {
		t.Fatalf("a dispatch with no attempts reported %d", got)
	}
	for want := uint64(1); want <= 3; want++ {
		if got := reg.Next("d1"); got != want {
			t.Fatalf("Next = %d, want %d", got, want)
		}
	}
	if got := reg.Next("d2"); got != 1 {
		t.Errorf("a second dispatch started at attempt %d, want 1", got)
	}
	if got := reg.Current("d1"); got != 3 {
		t.Errorf("Current = %d, want 3", got)
	}
	reg.Forget("d1")
	if got := reg.Current("d1"); got != 0 {
		t.Errorf("a forgotten dispatch reported attempt %d", got)
	}
}

// TestConcurrentRetriesNeverShareAnAttempt is the property fencing rests
// on: if two retries could mint the same number, both would consider
// themselves current and the refusal below would never fire.
func TestConcurrentRetriesNeverShareAnAttempt(t *testing.T) {
	const racers = 16
	reg := NewAttemptRegister()

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		seen = map[uint64]bool{}
	)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n := reg.Next("contended")
			mu.Lock()
			defer mu.Unlock()
			if seen[n] {
				t.Errorf("attempt %d was minted twice", n)
			}
			seen[n] = true
		}()
	}
	wg.Wait()

	if len(seen) != racers {
		t.Fatalf("minted %d distinct attempts for %d racers", len(seen), racers)
	}
	if got := reg.Current("contended"); got != racers {
		t.Errorf("Current = %d, want %d", got, racers)
	}
}

// fakeActionLog records reservations in memory.
type fakeActionLog struct {
	reserved   map[string]bool
	completed  map[string]DispatchOutcome
	reserveErr error
}

func newFakeActionLog() *fakeActionLog {
	return &fakeActionLog{reserved: map[string]bool{}, completed: map[string]DispatchOutcome{}}
}

func (f *fakeActionLog) Reserve(_ context.Context, actionID string) (bool, error) {
	if f.reserveErr != nil {
		return false, f.reserveErr
	}
	if f.reserved[actionID] {
		return true, nil
	}
	f.reserved[actionID] = true
	return false, nil
}

func (f *fakeActionLog) Complete(_ context.Context, actionID string, outcome DispatchOutcome) error {
	f.completed[actionID] = outcome
	return nil
}

// TestARedeliveredActionIsRefusedNotRerun is the dedup rule. The second
// delivery must be refused, and — the part that matters — refused as a
// CONFLICT rather than as a failure, because the work already happened.
func TestARedeliveredActionIsRefusedNotRerun(t *testing.T) {
	log := newFakeActionLog()
	action := Action{ID: "act-1"}

	if err := ReserveAction(context.Background(), log, action); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	err := ReserveAction(context.Background(), log, action)
	if err == nil {
		t.Fatal("a redelivered action was allowed to run again")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindConflict {
		t.Errorf("kind = %v (ok=%v), want KindConflict", kind, ok)
	}
}

// TestAnActionWithoutProtectionIsRefused proves dedup cannot be skipped by
// omission — no log, or no stable id, and the action does not run.
func TestAnActionWithoutProtectionIsRefused(t *testing.T) {
	if err := ReserveAction(context.Background(), nil, Action{ID: "act-1"}); err == nil {
		t.Error("an action ran with no dedup log wired")
	}
	if err := ReserveAction(context.Background(), newFakeActionLog(), Action{}); err == nil {
		t.Error("an action with no id was reserved; a redelivery could not be detected")
	}
}

// TestAmbiguousOutcomeHoldsNonIdempotentWork is the rule that must never
// become "retry and hope". An action that may have had an external effect
// is held for a human; only an explicitly idempotent one is releasable.
func TestAmbiguousOutcomeHoldsNonIdempotentWork(t *testing.T) {
	if err := ResolveAmbiguousOutcome("d1", Action{ID: "a1", Idempotent: true}); err != nil {
		t.Errorf("an idempotent action was held: %v", err)
	}
	err := ResolveAmbiguousOutcome("d1", Action{ID: "a1"})
	if err == nil {
		t.Fatal("a non-idempotent action with an unknown outcome was released for re-queue")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindConflict {
		t.Errorf("kind = %v (ok=%v), want KindConflict", kind, ok)
	}
}

// TestReserveActionPropagatesAStoreFailure proves a dedup log that cannot
// answer fails the dispatch rather than letting it run unprotected.
func TestReserveActionPropagatesAStoreFailure(t *testing.T) {
	log := newFakeActionLog()
	log.reserveErr = errors.New("disk is full")
	if err := ReserveAction(context.Background(), log, Action{ID: "a1"}); err == nil {
		t.Fatal("an action ran despite the dedup log failing")
	}
}

// TestNewAttemptCarriesItsOwnBranch proves the attempt and the ref agree,
// which is what makes the ref itself the fence.
func TestNewAttemptCarriesItsOwnBranch(t *testing.T) {
	reg := NewAttemptRegister()
	now := time.Unix(1700000000, 0).UTC()

	first := NewAttempt(reg, "d1", "node-a", now)
	second := NewAttempt(reg, "d1", "node-b", now)

	if first.Branch == second.Branch {
		t.Fatalf("two attempts shared branch %q; one would overwrite the other", first.Branch)
	}
	if first.Branch != DispatchBranch("d1", 1) || second.Branch != DispatchBranch("d1", 2) {
		t.Fatalf("branches = %q, %q", first.Branch, second.Branch)
	}
	if second.Attempt != 2 || second.NodeID != "node-b" || !second.StartedAt.Equal(now) {
		t.Fatalf("second attempt = %+v", second)
	}
}
