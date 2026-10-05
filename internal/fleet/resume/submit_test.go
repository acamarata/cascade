// Purpose: task 3 narrowed by EPIC Decision 12: a resumable fan-out cursor
//   is classified and never re-dispatched (no completed-leg replay, no
//   fence marker), including a journal that still holds a fence marker an
//   earlier version wrote. The kill -9 fan-out and upgrade-in-place tests
//   live in submit_kill9_test.go (R-14.117 authorized split).
// SPORT: internal.fleet.resume.ResumeManager/CHANGE (tests) (P1-CORE-15).

package resume

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/journal"
)

func seedFanOutCursor(t *testing.T, store journal.Store, taskID string, legs int, completedIdx ...int) {
	t.Helper()
	ctx := context.Background()
	cursorPayload := cursorPayloadFor(t, taskID, taskID, legs)
	if _, err := store.Append(ctx, FanOutEntity(taskID), journal.KindResumeCursor, "cursor-op", cursorPayload); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
	for _, idx := range completedIdx {
		done, err := json.Marshal(legPayload{LegIndex: idx, JobID: "job-" + itoa(uint64(idx)), Attempt: 1, Outcome: conductor.LegOutcomeOK})
		if err != nil {
			t.Fatalf("marshal leg: %v", err)
		}
		if _, err := store.Append(ctx, FanOutEntity(taskID), journal.KindFanOutLegDone, "leg-done-"+itoa(uint64(idx)), done); err != nil {
			t.Fatalf("seed leg done: %v", err)
		}
	}
}

func TestResumeFanOutCursorClassifiedNotDispatched(t *testing.T) {
	store, _, _ := newRealStore(t)
	seedFanOutCursor(t, store, "t-3legs", 3, 0, 1) // legs 0,1 done; leg 2 unfinished
	mgr, err := New(store, nil, nil, "darwin")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := mgr.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertClassifiedOnly(t, store, report, FanOutEntity("t-3legs"), 3)
}

// TestResumeLegacyFenceMarkerIgnored: a fence marker an earlier version
// appended rides KindResumeCursor; classification skips it and Run still
// appends nothing.
func TestResumeLegacyFenceMarkerIgnored(t *testing.T) {
	store, _, _ := newRealStore(t)
	seedFanOutCursor(t, store, "t-fenced", 2)
	fence, err := json.Marshal(fenceMarker{T: "fence", ActionID: FanOutEntity("t-fenced")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), FanOutEntity("t-fenced"), journal.KindResumeCursor, "legacy-fence", fence); err != nil {
		t.Fatalf("seed fence: %v", err)
	}
	mgr, err := New(store, nil, nil, "darwin")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := mgr.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertClassifiedOnly(t, store, report, FanOutEntity("t-fenced"), 2)
}
