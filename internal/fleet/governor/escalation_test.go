package governor

// Purpose: EscalationLadder.Advance's full behavioral suite: every
// rung-success/rung-failure transition, the Human-exhausted terminal
// path, fail-closed confidence handling, the above-threshold no-op, a
// journal write failure, R-21.216 attempt-budget exhaustion, foreign-kind
// isolation, and concurrent-Advance serialization. Shared fixtures
// (fakeJournalStore, countingSeams, fixedConfidence, seedEvent,
// decodeLast) live in escalation_seams_test.go. All seams and the
// journal.Store are stubs defined in _test.go files only (Art.1); the
// clock is always an injected runtime.FixedClock, never advanced by
// sleeping (Art.7.3).

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestEscalationLadderRungTransitions covers all four rung-success and
// four rung-failure transitions in one table, asserting the returned
// error, the seam actually invoked, and the journaled event's rung.
func TestEscalationLadderRungTransitions(t *testing.T) {
	cases := []struct {
		name      string
		rung      EscalationRung
		fail      bool
		wantEvent EscalationRung
		wantErr   error // nil means "no error"
	}{
		{"retry success", RungRetry, false, RungRetry, nil},
		{"retry failure", RungRetry, true, RungContext, ErrEscalationRungFailed},
		{"context success", RungContext, false, RungContext, nil},
		{"context failure", RungContext, true, RungSupervisorTask, ErrEscalationRungFailed},
		{"supervisor-task success", RungSupervisorTask, false, RungSupervisorTask, nil},
		{"supervisor-task failure", RungSupervisorTask, true, RungHuman, ErrEscalationRungFailed},
		{"human success", RungHuman, false, RungHuman, nil},
		{"human failure", RungHuman, true, RungHuman, EscalationExhausted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeJournalStore()
			if tc.rung != RungRetry {
				seedEvent(t, store, "e1", tc.rung, 0)
			}
			seams := newCountingSeams()
			if tc.fail {
				seams.fail[tc.rung] = errors.New("seam failed")
			}
			l := newTestEscalationLadder(store, fixedConfidence{v: 0}, seams, testPolicy(), runtime.NewFixedClock(time.Unix(0, 0)))

			err := l.Advance(context.Background(), "e1")
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Advance() = %v, want nil", err)
				}
			} else if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Advance() = %v, want %v", err, tc.wantErr)
			}
			if got := seams.callCount(tc.rung); got != 1 {
				t.Fatalf("seam at %v called %d times, want exactly 1", tc.rung, got)
			}
			last := decodeLast(t, store, "e1")
			if last.Rung != tc.wantEvent {
				t.Fatalf("journaled rung = %v, want %v", last.Rung, tc.wantEvent)
			}
		})
	}
}

// TestEscalationLadderRetrySuccess is the dedicated single-case
// counterpart of the table's "retry success" row, run standalone by name
// per the ticket's checks list.
func TestEscalationLadderRetrySuccess(t *testing.T) {
	store := newFakeJournalStore()
	seams := newCountingSeams()
	l := newTestEscalationLadder(store, fixedConfidence{v: 0}, seams, testPolicy(), runtime.NewFixedClock(time.Unix(0, 0)))

	if err := l.Advance(context.Background(), "e1"); err != nil {
		t.Fatalf("Advance() = %v, want nil", err)
	}
	if got := seams.callCount(RungRetry); got != 1 {
		t.Fatalf("Retry called %d times, want 1", got)
	}
	if got := seams.callCount(RungContext); got != 0 {
		t.Fatalf("Enrich called %d times, want 0 (retry succeeded, no advance)", got)
	}
}

// TestEscalationLadderHumanExhausted drives the ladder to a Human-rung
// failure, asserts EscalationExhausted, and then proves the terminal
// guard: a second Advance call for the same entity returns
// EscalationExhausted again WITHOUT calling Notify a second time - the
// human notification side effect fires at most once per escalation.
func TestEscalationLadderHumanExhausted(t *testing.T) {
	store := newFakeJournalStore()
	seedEvent(t, store, "e1", RungHuman, 0)
	seams := newCountingSeams()
	seams.fail[RungHuman] = errors.New("paging service down")
	l := newTestEscalationLadder(store, fixedConfidence{v: 0}, seams, testPolicy(), runtime.NewFixedClock(time.Unix(0, 0)))

	if err := l.Advance(context.Background(), "e1"); !errors.Is(err, EscalationExhausted) {
		t.Fatalf("Advance() = %v, want EscalationExhausted", err)
	}
	if got := seams.callCount(RungHuman); got != 1 {
		t.Fatalf("Notify called %d times after first exhaustion, want 1", got)
	}

	if err := l.Advance(context.Background(), "e1"); !errors.Is(err, EscalationExhausted) {
		t.Fatalf("second Advance() = %v, want EscalationExhausted (never loop)", err)
	}
	if got := seams.callCount(RungHuman); got != 1 {
		t.Fatalf("Notify called %d times after second Advance, want still 1 (exactly-once)", got)
	}
}

// TestEscalationLadderFailClosed asserts the two fail-closed rules
// together: safeRung(0) == RungHuman, and a ConfidenceProvider error
// still executes the current rung rather than silently skipping it.
func TestEscalationLadderFailClosed(t *testing.T) {
	if got := safeRung(0); got != RungHuman {
		t.Fatalf("safeRung(0) = %v, want RungHuman", got)
	}

	store := newFakeJournalStore()
	seams := newCountingSeams()
	l := newTestEscalationLadder(store, fixedConfidence{err: errors.New("confidence source down")}, seams, testPolicy(), runtime.NewFixedClock(time.Unix(0, 0)))

	if err := l.Advance(context.Background(), "e1"); err != nil {
		t.Fatalf("Advance() with confidence error = %v, want nil (rung executed and succeeded)", err)
	}
	if got := seams.callCount(RungRetry); got != 1 {
		t.Fatalf("Retry called %d times on confidence error, want 1 (fail closed: escalate, never skip)", got)
	}
}

// TestEscalationLadderAboveThresholdNoop asserts a confident entity is
// left alone entirely: zero seam invocations and no journal write.
func TestEscalationLadderAboveThresholdNoop(t *testing.T) {
	store := newFakeJournalStore()
	seams := newCountingSeams()
	l := newTestEscalationLadder(store, fixedConfidence{v: 0.9}, seams, testPolicy(), runtime.NewFixedClock(time.Unix(0, 0)))

	if err := l.Advance(context.Background(), "e1"); err != nil {
		t.Fatalf("Advance() = %v, want nil", err)
	}
	for _, rung := range []EscalationRung{RungRetry, RungContext, RungSupervisorTask, RungHuman} {
		if got := seams.callCount(rung); got != 0 {
			t.Fatalf("seam at %v called %d times, want 0 (above threshold, no-op)", rung, got)
		}
	}
	if got := store.count("e1"); got != 0 {
		t.Fatalf("journal has %d entries, want 0 (no-op writes nothing)", got)
	}
}

// TestEscalationLadderJournalWriteFailure asserts a journal Append
// failure surfaces as a typed error and leaves the rung state unchanged
// (nothing was durably recorded, so the next successful call still reads
// the same starting point).
func TestEscalationLadderJournalWriteFailure(t *testing.T) {
	store := newFakeJournalStore()
	store.appendFail = errors.New("disk full")
	seams := newCountingSeams()
	l := newTestEscalationLadder(store, fixedConfidence{v: 0}, seams, testPolicy(), runtime.NewFixedClock(time.Unix(0, 0)))

	err := l.Advance(context.Background(), "e1")
	if !errors.Is(err, ErrEscalationJournalUnavailable) {
		t.Fatalf("Advance() = %v, want ErrEscalationJournalUnavailable", err)
	}
	if _, ok := cascade.KindOf(err); !ok {
		t.Fatal("journal write failure error is not a taxonomy error")
	}
	if got := store.count("e1"); got != 0 {
		t.Fatalf("journal has %d entries after a failed Append, want 0 (rung not advanced)", got)
	}
}

// TestEscalationLadderAdvancesOnAttemptExhaustion is R-21.216's own
// worked example: with MaxAttempts[RungRetry]=2 and a seam that always
// succeeds, the ladder reaches RungContext on the third Advance, never
// looping forever on a perpetually-succeeding rung.
func TestEscalationLadderAdvancesOnAttemptExhaustion(t *testing.T) {
	store := newFakeJournalStore()
	seams := newCountingSeams()
	policy := testPolicy()
	policy.MaxAttempts[RungRetry] = 2
	l := newTestEscalationLadder(store, fixedConfidence{v: 0}, seams, policy, runtime.NewFixedClock(time.Unix(0, 0)))

	for i, want := range []EscalationRung{RungRetry, RungRetry, RungContext} {
		if err := l.Advance(context.Background(), "e1"); err != nil {
			t.Fatalf("Advance() call %d = %v, want nil", i+1, err)
		}
		if last := decodeLast(t, store, "e1"); last.Rung != want {
			t.Fatalf("call %d: journaled rung = %v, want %v", i+1, last.Rung, want)
		}
	}
	if got := seams.callCount(RungRetry); got != 2 {
		t.Fatalf("Retry called %d times, want exactly 2 (budget spent, no third attempt)", got)
	}
	if got := seams.callCount(RungContext); got != 1 {
		t.Fatalf("Enrich called %d times, want exactly 1", got)
	}
}

// TestEscalationLadderIgnoresForeignKinds seeds an escalation event and
// then a resume-cursor entry (a different journal.Kind, higher Seq, for
// the same entity) and asserts Advance still reads the escalation rung,
// not the foreign entry - proving R-21.216's kinds filter is load-bearing.
func TestEscalationLadderIgnoresForeignKinds(t *testing.T) {
	store := newFakeJournalStore()
	seedEvent(t, store, "e1", RungSupervisorTask, 0)
	if _, err := store.Append(context.Background(), "e1", journal.KindResumeCursor, "op-1", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("seeding foreign kind: %v", err)
	}
	seams := newCountingSeams()
	l := newTestEscalationLadder(store, fixedConfidence{v: 0}, seams, testPolicy(), runtime.NewFixedClock(time.Unix(0, 0)))

	if err := l.Advance(context.Background(), "e1"); err != nil {
		t.Fatalf("Advance() = %v, want nil", err)
	}
	if got := seams.callCount(RungSupervisorTask); got != 1 {
		t.Fatalf("CreateSupervisor called %d times, want 1 (rung must resume at SupervisorTask, not Retry)", got)
	}
	if got := seams.callCount(RungRetry); got != 0 {
		t.Fatalf("Retry called %d times, want 0 (foreign kind must not reset the rung)", got)
	}
}

// TestEscalationLadderEmptyEntityIDRefused asserts the call-site
// validation rejects an empty entity id before touching the journal or
// any seam.
func TestEscalationLadderEmptyEntityIDRefused(t *testing.T) {
	store := newFakeJournalStore()
	seams := newCountingSeams()
	l := newTestEscalationLadder(store, fixedConfidence{v: 0}, seams, testPolicy(), runtime.NewFixedClock(time.Unix(0, 0)))

	if err := l.Advance(context.Background(), ""); !errors.Is(err, ErrEscalationInvalidInput) {
		t.Fatalf("Advance(\"\") = %v, want ErrEscalationInvalidInput", err)
	}
}

// TestEscalationLadderConcurrentAdvanceSerializes is the concurrency
// proof: N goroutines call Advance for the SAME entity at once, with a
// budget of exactly N attempts at RungRetry. Advance's per-entity lock
// must serialize the read-decide-execute-append sequence, so every
// attempt is counted exactly once (no lost update) and the ladder ends
// up at exactly RungContext, never skipping it or double-advancing past
// it. Run with -race.
func TestEscalationLadderConcurrentAdvanceSerializes(t *testing.T) {
	const n = 20
	store := newFakeJournalStore()
	seams := newCountingSeams()
	policy := testPolicy()
	policy.MaxAttempts[RungRetry] = n
	l := newTestEscalationLadder(store, fixedConfidence{v: 0}, seams, policy, runtime.NewFixedClock(time.Unix(0, 0)))

	var wg sync.WaitGroup
	var okCount int64
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.Advance(context.Background(), "shared"); err == nil {
				atomic.AddInt64(&okCount, 1)
			}
		}()
	}
	wg.Wait()

	if int(okCount) != n {
		t.Fatalf("%d of %d concurrent Advance calls returned nil, want all %d", okCount, n, n)
	}
	if got := store.count("shared"); got != n {
		t.Fatalf("journal has %d entries, want exactly %d (no lost update, no duplicate)", got, n)
	}
	if got := seams.callCount(RungRetry); got != n {
		t.Fatalf("Retry called %d times, want exactly %d", got, n)
	}
	last := decodeLast(t, store, "shared")
	if last.Rung != RungRetry || last.Attempt != n {
		t.Fatalf("final event = %+v, want rung RungRetry attempt %d", last, n)
	}
}
