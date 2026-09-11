// Purpose: tasks 3-4's re-submission proofs: completed-leg skip
//   (R-21.214) and fenced/deduplicated dispatch (R-21.221). The kill -9
//   fan-out and upgrade-in-place integration tests live in
//   submit_kill9_test.go (R-14.117 authorized split, Art.10.3's
//   300-line-per-file cap).
// SPORT: internal.fleet.resume.ResumeManager/ADDED (tests) (P1-E13-W3-S27-T2).

package resume

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/pkg/provider"
)

func seedFanOutCursor(t *testing.T, store journal.Store, taskID string, legs int, completedIdx ...int) {
	t.Helper()
	ctx := context.Background()
	req, err := json.Marshal(provider.ModelRequest{TaskID: taskID, TaskClass: "chat", Inputs: []provider.ChatMessage{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	cursorPayload, err := json.Marshal(resumeCursorPayload{T: "cursor", TaskID: taskID, Legs: legs, Request: req})
	if err != nil {
		t.Fatalf("marshal cursor: %v", err)
	}
	if _, err := store.Append(ctx, taskID, journal.KindResumeCursor, "cursor-op", cursorPayload); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
	for _, idx := range completedIdx {
		done, err := json.Marshal(legPayload{LegIndex: idx, JobID: "job-" + itoa(uint64(idx)), Attempt: 1})
		if err != nil {
			t.Fatalf("marshal leg: %v", err)
		}
		if _, err := store.Append(ctx, taskID, journal.KindFanOutLegDone, "leg-done-"+itoa(uint64(idx)), done); err != nil {
			t.Fatalf("seed leg done: %v", err)
		}
	}
}

func TestResumeSkipsCompletedFanOutLegs(t *testing.T) {
	store, _, _ := newRealStore(t)
	seedFanOutCursor(t, store, "t-3legs", 3, 0, 1) // legs 0,1 done; leg 2 unfinished

	var calls []fakeFanOutCall
	mgr, err := New(store, fakeFanOut(&calls, []provider.ModelResponse{{JobID: "job-0"}, {JobID: "job-1"}, {JobID: "job-2"}}, nil), nil, nil, nil, nil, "darwin")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := mgr.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("fanOut called %d times, want exactly 1 (completed legs skipped, R-21.214)", len(calls))
	}
	if len(calls[0].completed) != 2 {
		t.Fatalf("completed map = %+v, want 2 entries (legs 0 and 1)", calls[0].completed)
	}
	if _, ok := calls[0].completed[0]; !ok {
		t.Error("completed map missing leg 0")
	}
	if _, ok := calls[0].completed[1]; !ok {
		t.Error("completed map missing leg 1")
	}
	if len(report.Outcomes) != 1 || report.Outcomes[0].LegsDispatched != 1 {
		t.Fatalf("Outcomes = %+v, want LegsDispatched=1 (only leg 2 unfinished)", report.Outcomes)
	}
}

func TestResumeFencedAttemptRejectsStale(t *testing.T) {
	store, _, _ := newRealStore(t)
	ctx := context.Background()
	seedFanOutCursor(t, store, "t-race", 1)

	blockCh := make(chan struct{})
	proceedCh := make(chan struct{})
	first := true
	fo := func(_ context.Context, _ provider.ModelRequest, _ int, _ map[int]conductor.JobID, _ conductor.WithPermitFn, _ conductor.JournalAppender) ([]provider.ModelResponse, error) {
		if first {
			first = false
			close(blockCh)
			<-proceedCh // wait until the "concurrent, newer" claim has landed
		}
		return []provider.ModelResponse{{JobID: "job-0"}}, nil
	}
	mgr, err := New(store, fo, nil, nil, nil, nil, "darwin")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cursor := resumeCursor{TaskID: "t-race", Kind: cursorFanOut, Request: provider.ModelRequest{TaskID: "t-race", Inputs: []provider.ChatMessage{{Role: "user", Content: "hi"}}}, Legs: 1, Completed: map[int]conductor.JobID{}}

	type result struct {
		dispatched int
		err        error
	}
	resCh := make(chan result, 1)
	go func() {
		d, err := mgr.resubmit(ctx, cursor)
		resCh <- result{d, err}
	}()

	<-blockCh
	// A second, "newer" resumer claims a fresher attempt for the SAME
	// task while the first call's fan-out dispatch is still in flight.
	if _, err := mgr.claimAttempt(ctx, "t-race", "fanout:t-race"); err != nil {
		t.Fatalf("claimAttempt (concurrent): %v", err)
	}
	close(proceedCh)

	assertStaleAttemptDiscarded(ctx, t, store, <-resCh)
}

// assertStaleAttemptDiscarded is split out of
// TestResumeFencedAttemptRejectsStale to stay under the 50-line function
// cap (funlen): it checks the superseded attempt's own result and that
// exactly one discard record landed in the journal.
func assertStaleAttemptDiscarded(ctx context.Context, t *testing.T, store journal.Store, res struct {
	dispatched int
	err        error
}) {
	t.Helper()
	if res.err != ErrStaleAttempt {
		t.Fatalf("first resubmit's result = (%d, %v), want (0, ErrStaleAttempt)", res.dispatched, res.err)
	}
	if res.dispatched != 0 {
		t.Fatalf("stale result reported %d legs dispatched, want 0 (discarded, never applied)", res.dispatched)
	}
	discards, err := store.Replay(ctx, "t-race", journal.Cursor{EntityID: "t-race", Seq: 0}, []journal.Kind{journal.KindAck})
	if err != nil {
		t.Fatalf("Replay for discard record: %v", err)
	}
	if len(discards) != 1 {
		t.Fatalf("Ack (discard) entries = %d, want 1 (stale result journaled and discarded)", len(discards))
	}
}
