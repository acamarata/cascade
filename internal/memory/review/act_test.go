// Purpose: the four review actions under test — that each one does
//
//	exactly what it says, that a skip changes nothing at all, that a defer
//	hides a candidate for precisely as long as it claims (frozen clock,
//	both sides of the boundary), that a revert leaves the counter restarted
//	per R-14.22, and that every refusal is a typed taxonomy error.
//
// SPORT: internal/memory/review (ADD, P1-E07-W2-S14-T3).
package review

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/pkg/cascade"
)

// act runs one action, failing the test on refusal.
func (f fixture) act(t *testing.T, p ActParams) ActResult {
	t.Helper()
	got, err := f.queue.Act(context.Background(), p)
	if err != nil {
		t.Fatalf("Act(%+v): %v", p, err)
	}
	return got
}

func TestApprovePromotesABelowThresholdCandidate(t *testing.T) {
	f := newFixture(t)
	f.observe(t, memory.KindProject, "below", "s-1")

	got := f.act(t, ActParams{ID: "project/below", Action: ActionApprove})

	if !got.Changed || got.Item.Status != memory.CandidatePromoted {
		t.Fatalf("result = %+v, want a promotion", got)
	}
	// The durable record is really there: promotion is the one-way door,
	// and a test that only read the candidate back would not have proved
	// the door opened.
	entry, err := f.store.Read(context.Background(), memory.KindProject, "below")
	if err != nil {
		t.Fatalf("the approved candidate is not in the store: %v", err)
	}
	if entry.Body != draft(memory.KindProject, "below").Body {
		t.Errorf("stored body = %q, want the candidate's draft", entry.Body)
	}
	listed := f.list(t, ListParams{})
	if len(listed.Pending) != 0 {
		t.Errorf("pending = %v, want the approved candidate gone from the queue", ids(listed.Pending))
	}
	if want := []string{"project/below"}; !equalStrings(ids(listed.Promoted), want) {
		t.Errorf("promoted = %v, want %v", ids(listed.Promoted), want)
	}
	if len(f.sink.actions) != 1 || f.sink.actions[0].Action != ActionApprove ||
		!f.sink.actions[0].Changed {
		t.Errorf("audit events = %+v, want one recorded approve", f.sink.actions)
	}
}

func TestSkipLeavesTheCandidateExactlyAsItWas(t *testing.T) {
	f := newFixture(t)
	f.observe(t, memory.KindProject, "below", "s-1")
	before := treeDigest(t, f.base)

	got := f.act(t, ActParams{ID: "project/below", Action: ActionSkip})

	if got.Changed {
		t.Error("a skip reported a change")
	}
	if got.Item.RefCount != 1 || got.Item.Status != memory.CandidatePending {
		t.Errorf("item = %+v, want it untouched", got.Item)
	}
	if after := treeDigest(t, f.base); after != before {
		t.Fatalf("a skip changed the store: %s -> %s", before, after)
	}
	listed := f.list(t, ListParams{})
	if want := []string{"project/below"}; !equalStrings(ids(listed.Pending), want) {
		t.Errorf("pending = %v, want the skipped candidate still listed", ids(listed.Pending))
	}
	// A skip is still recorded: the audit answers "did anyone look".
	if len(f.sink.actions) != 1 || f.sink.actions[0].Changed {
		t.Errorf("audit events = %+v, want one skip recorded as changing nothing", f.sink.actions)
	}
}

// TestDeferHidesUntilTheSnoozeExpires walks both sides of the boundary on
// a frozen clock. The moment of expiry is the whole contract of the
// action, so it is asserted rather than approximated.
func TestDeferHidesUntilTheSnoozeExpires(t *testing.T) {
	f := newFixture(t)
	f.observe(t, memory.KindProject, "noisy", "s-1")

	got := f.act(t, ActParams{ID: "project/noisy", Action: ActionDefer, DeferDays: 3})

	want := fixedNow.AddDate(0, 0, 3)
	if got.Item.SnoozeUntil == nil || !got.Item.SnoozeUntil.Equal(want) {
		t.Fatalf("snooze until = %v, want %s", got.Item.SnoozeUntil, want)
	}
	if hidden := f.list(t, ListParams{}); len(hidden.Pending) != 0 || hidden.Snoozed != 1 {
		t.Fatalf("during the snooze: pending=%v snoozed=%d, want it hidden but counted",
			ids(hidden.Pending), hidden.Snoozed)
	}
	// One instant before expiry it is still hidden.
	f.clock.Set(want.Add(-time.Nanosecond))
	if still := f.list(t, ListParams{}); len(still.Pending) != 0 {
		t.Fatalf("a nanosecond before expiry it reappeared: %v", ids(still.Pending))
	}
	// At expiry it is back, with its counts untouched.
	f.clock.Set(want)
	back := f.list(t, ListParams{})
	if len(back.Pending) != 1 || back.Snoozed != 0 {
		t.Fatalf("at expiry: pending=%v snoozed=%d, want it back", ids(back.Pending), back.Snoozed)
	}
	if back.Pending[0].RefCount != 1 {
		t.Errorf("ref count = %d, want a defer to have changed no evidence", back.Pending[0].RefCount)
	}
}

func TestDeferDefaultsToOneWeekAndRefusesAnOutOfRangeWindow(t *testing.T) {
	f := newFixture(t)
	f.observe(t, memory.KindProject, "noisy", "s-1")

	got := f.act(t, ActParams{ID: "project/noisy", Action: ActionDefer})
	want := fixedNow.AddDate(0, 0, DefaultDeferDays)
	if got.Item.SnoozeUntil == nil || !got.Item.SnoozeUntil.Equal(want) {
		t.Errorf("default snooze = %v, want %s", got.Item.SnoozeUntil, want)
	}

	for _, days := range []int{-1, maxDeferDays + 1} {
		_, err := f.queue.Act(context.Background(),
			ActParams{ID: "project/noisy", Action: ActionDefer, DeferDays: days})
		if !errors.Is(err, ErrInvalidDeferDays) {
			t.Errorf("defer %d days: err = %v, want ErrInvalidDeferDays", days, err)
		}
		if kind, _ := cascade.KindOf(err); kind != cascade.KindInvalidInput {
			t.Errorf("defer %d days: kind = %v, want invalid_input", days, kind)
		}
	}
}

// TestRevertTakesBackAPromotionAndRestartsTheCount pins R-14.22's
// downstream semantics: after a revert the next observation starts from
// one, so a reverted belief has to earn its way back.
func TestRevertTakesBackAPromotionAndRestartsTheCount(t *testing.T) {
	f := newFixture(t)
	f.promote(t, memory.KindUser, "wrong")

	got := f.act(t, ActParams{ID: "user/wrong", Action: ActionRevert})

	if !got.Changed || got.Item.Status != memory.CandidateReverted {
		t.Fatalf("result = %+v, want a revert", got)
	}
	listed := f.list(t, ListParams{})
	if len(listed.Promoted) != 0 {
		t.Errorf("promoted = %v, want the reverted candidate gone", ids(listed.Promoted))
	}
	if len(listed.Pending) != 0 {
		t.Errorf("pending = %v, want a reverted candidate to be in neither section "+
			"until it is observed again", ids(listed.Pending))
	}
	after := f.observe(t, memory.KindUser, "wrong", "s-9")
	if after.RefCount != 1 || len(after.SessionIDs) != 1 {
		t.Errorf("after revert the counter is %+v, want it restarted at one (R-14.22)", after)
	}
}

func TestActRefusesWhatTheStatusDoesNotAdmit(t *testing.T) {
	f := newFixture(t)
	f.observe(t, memory.KindProject, "pending-one", "s-1")
	f.promote(t, memory.KindUser, "standing")

	cases := map[string]struct {
		params   ActParams
		sentinel error
		kind     cascade.Kind
	}{
		"revert a candidate that was never promoted": {
			params:   ActParams{ID: "project/pending-one", Action: ActionRevert},
			sentinel: memory.ErrNotPromoted, kind: cascade.KindConflict,
		},
		"approve an already promoted candidate": {
			params:   ActParams{ID: "user/standing", Action: ActionApprove},
			sentinel: memory.ErrAlreadyPromoted, kind: cascade.KindConflict,
		},
		"defer a promoted candidate": {
			params:   ActParams{ID: "user/standing", Action: ActionDefer, DeferDays: 1},
			sentinel: memory.ErrNotPending, kind: cascade.KindConflict,
		},
		"act on an address with no candidate": {
			params:   ActParams{ID: "project/absent", Action: ActionApprove},
			sentinel: memory.ErrNoSuchCandidate, kind: cascade.KindNotFound,
		},
		"skip an address with no candidate": {
			params:   ActParams{ID: "project/absent", Action: ActionSkip},
			sentinel: memory.ErrNoSuchCandidate, kind: cascade.KindNotFound,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.queue.Act(context.Background(), tc.params)
			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("err = %v, want %v", err, tc.sentinel)
			}
			if kind, _ := cascade.KindOf(err); kind != tc.kind {
				t.Errorf("kind = %v, want %v", kind, tc.kind)
			}
		})
	}
}

func TestActRefusesAnUnknownActionOrAnUnusableAddress(t *testing.T) {
	f := newFixture(t)
	f.observe(t, memory.KindProject, "below", "s-1")
	before := treeDigest(t, f.base)

	_, err := f.queue.Act(context.Background(),
		ActParams{ID: "project/below", Action: "forget-it"})
	if !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("unknown action err = %v, want ErrUnknownAction", err)
	}
	for _, id := range []string{"", "no-separator", "nosuchkind/name", "project/../escape"} {
		if _, err := f.queue.Act(context.Background(),
			ActParams{ID: id, Action: ActionApprove}); err == nil {
			t.Errorf("address %q was accepted", id)
		} else if kind, _ := cascade.KindOf(err); kind != cascade.KindInvalidInput {
			t.Errorf("address %q: kind = %v, want invalid_input", id, kind)
		}
	}
	if after := treeDigest(t, f.base); after != before {
		t.Fatalf("a refused action changed the store: %s -> %s", before, after)
	}
	if len(f.sink.actions) != 0 {
		t.Errorf("a refused action was recorded as an action: %+v", f.sink.actions)
	}
}

// TestQueueWithNoSinkStillActs covers the documented no-bus
// configuration: discarding events must not change what is stored.
func TestQueueWithNoSinkStillActs(t *testing.T) {
	f := newFixture(t)
	quiet := NewQueue(f.ledger, f.clock, nil)
	f.observe(t, memory.KindProject, "below", "s-1")

	got, err := quiet.Act(context.Background(),
		ActParams{ID: "project/below", Action: ActionApprove})
	if err != nil {
		t.Fatalf("approve with no sink: %v", err)
	}
	if got.Item.Status != memory.CandidatePromoted {
		t.Errorf("status = %q, want promoted", got.Item.Status)
	}
}
