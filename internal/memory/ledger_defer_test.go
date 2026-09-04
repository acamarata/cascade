package memory

// Purpose: Defer under test — that it writes SnoozeUntil and NOTHING else,
//   that it refuses a candidate whose status does not admit a deferral,
//   that it refuses a snooze that has already expired, and that a deferred
//   candidate still promotes mechanically when the evidence arrives.
// SPORT: internal.memory.FileCandidateLedger.Defer (ADD, P1-E07-W2-S14-T3).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestDeferWritesOnlyTheSnooze(t *testing.T) {
	f := newLedger(t)
	before := observeAll(t, f.ledger, "s-1", "s-2")
	until := fixedNow.Add(48 * time.Hour)

	got, err := f.ledger.Defer(context.Background(), KindProject, before.Name, until)
	if err != nil {
		t.Fatalf("Defer: %v", err)
	}

	if got.SnoozeUntil == nil || !got.SnoozeUntil.Equal(until) {
		t.Fatalf("snooze until = %v, want %s", got.SnoozeUntil, until)
	}
	if got.RefCount != before.RefCount || len(got.SessionIDs) != len(before.SessionIDs) {
		t.Errorf("a defer changed the evidence: %+v, want %+v", got, before)
	}
	if got.Status != CandidatePending {
		t.Errorf("status = %q, want it still pending", got.Status)
	}
	// The write is durable, not merely returned.
	read, err := f.ledger.Get(context.Background(), KindProject, before.Name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if read.SnoozeUntil == nil || !read.SnoozeUntil.Equal(until) {
		t.Errorf("re-read snooze = %v, want it persisted", read.SnoozeUntil)
	}
}

// TestDeferDoesNotStopTheMechanicalLane pins the division of
// responsibility: a defer is a statement about a human's attention, not
// about the evidence, so the ladder still promotes on the same terms.
func TestDeferDoesNotStopTheMechanicalLane(t *testing.T) {
	f := newLedger(t)
	entry := observeAll(t, f.ledger, "s-1")
	if _, err := f.ledger.Defer(context.Background(), KindProject, entry.Name,
		fixedNow.Add(30*24*time.Hour)); err != nil {
		t.Fatalf("Defer: %v", err)
	}

	ladder := NewPromotionLadder(f.ledger)
	var promoted bool
	for _, session := range []string{"s-2", "s-3"} {
		var err error
		_, promoted, err = ladder.Observe(context.Background(), observation(session))
		if err != nil {
			t.Fatalf("Observe(%s): %v", session, err)
		}
	}
	if !promoted {
		t.Fatal("a deferred candidate that crossed the threshold was not promoted")
	}
}

func TestDeferRefusals(t *testing.T) {
	f := newLedger(t)
	entry := observeAll(t, f.ledger, "s-1")

	t.Run("a snooze that is not in the future", func(t *testing.T) {
		_, err := f.ledger.Defer(context.Background(), KindProject, entry.Name, fixedNow)
		if !errors.Is(err, ErrSnoozeInThePast) {
			t.Fatalf("err = %v, want ErrSnoozeInThePast", err)
		}
		if kind, _ := cascade.KindOf(err); kind != cascade.KindInvalidInput {
			t.Errorf("kind = %v, want invalid_input", kind)
		}
	})

	t.Run("no such candidate", func(t *testing.T) {
		_, err := f.ledger.Defer(context.Background(), KindProject, "absent",
			fixedNow.Add(time.Hour))
		if !errors.Is(err, ErrNoSuchCandidate) {
			t.Fatalf("err = %v, want ErrNoSuchCandidate", err)
		}
	})

	t.Run("a promoted candidate", func(t *testing.T) {
		if _, err := f.ledger.Promote(context.Background(), KindProject, entry.Name); err != nil {
			t.Fatalf("Promote: %v", err)
		}
		_, err := f.ledger.Defer(context.Background(), KindProject, entry.Name,
			fixedNow.Add(time.Hour))
		if !errors.Is(err, ErrNotPending) {
			t.Fatalf("err = %v, want ErrNotPending", err)
		}
		if kind, _ := cascade.KindOf(err); kind != cascade.KindConflict {
			t.Errorf("kind = %v, want conflict", kind)
		}
	})

	t.Run("a canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := f.ledger.Defer(ctx, KindProject, entry.Name,
			fixedNow.Add(time.Hour)); err == nil {
			t.Fatal("a canceled context still wrote a snooze")
		}
	})
}
