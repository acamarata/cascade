package supervision

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

func TestProgressTrackerHealthyWithinThreshold(t *testing.T) {
	clock := runtime.NewFixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	tr := NewProgressTracker(clock, time.Minute)
	tr.Touch("sess-1")
	clock.Advance(30 * time.Second)
	status, _ := tr.Status("sess-1")
	if status != ProgressHealthy {
		t.Fatalf("Status = %s, want healthy", status)
	}
}

// TestProgressTrackerStalledPastThreshold proves "not seen recently" is
// a real, known conclusion (ProgressStalled), distinct from
// ProgressUnknown, once elapsed time exceeds threshold for a session the
// tracker HAS observed.
func TestProgressTrackerStalledPastThreshold(t *testing.T) {
	clock := runtime.NewFixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	tr := NewProgressTracker(clock, time.Minute)
	tr.Touch("sess-1")
	clock.Advance(2 * time.Minute)
	status, since := tr.Status("sess-1")
	if status != ProgressStalled {
		t.Fatalf("Status = %s, want stalled", status)
	}
	if since == 0 {
		t.Error("since = 0 for a stalled (known) session, want the real last-touch instant")
	}
}

// TestProgressTrackerUnknownWhenNeverTouched proves "could not tell" is
// reported for a session the tracker has never observed at all, never
// silently as healthy or as stalled.
func TestProgressTrackerUnknownWhenNeverTouched(t *testing.T) {
	clock := runtime.NewFixedClock(time.Now())
	tr := NewProgressTracker(clock, time.Minute)
	status, since := tr.Status("never-seen")
	if status != ProgressUnknown {
		t.Fatalf("Status = %s, want unknown", status)
	}
	if since != 0 {
		t.Errorf("since = %d, want 0 for an unknown session", since)
	}
}

// TestProgressTrackerUnknownWhenSourceUnavailable proves that a stale or
// absent SOURCE overrides even a recently-touched session's own history:
// "could not tell" is not merely "never touched", it also covers "the
// subscription that would tell us is currently dead".
func TestProgressTrackerUnknownWhenSourceUnavailable(t *testing.T) {
	clock := runtime.NewFixedClock(time.Now())
	tr := NewProgressTracker(clock, time.Minute)
	tr.Touch("sess-1")
	tr.MarkSourceUnavailable()
	status, _ := tr.Status("sess-1")
	if status != ProgressUnknown {
		t.Fatalf("Status = %s, want unknown while source is unavailable", status)
	}
	tr.MarkSourceAvailable()
	status, _ = tr.Status("sess-1")
	if status != ProgressHealthy {
		t.Fatalf("Status = %s, want healthy once source recovers", status)
	}
}

// TestConfidenceMapsStatusToProviderContract proves the three distinct
// tracker conclusions produce three distinct governor.ConfidenceProvider
// answers, and that ProgressUnknown is an ERROR (fail-closed to "stuck"),
// never a silent 1.0 (healthy) or a value indistinguishable from a real
// stall.
func TestConfidenceMapsStatusToProviderContract(t *testing.T) {
	clock := runtime.NewFixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	tr := NewProgressTracker(clock, time.Minute)
	ctx := context.Background()

	tr.Touch("healthy-sess")
	if conf, err := tr.Confidence(ctx, "healthy-sess"); err != nil || conf != 1 {
		t.Errorf("healthy: Confidence = (%v, %v), want (1, nil)", conf, err)
	}

	tr.Touch("stalled-sess")
	clock.Advance(2 * time.Minute)
	if conf, err := tr.Confidence(ctx, "stalled-sess"); err != nil || conf != 0 {
		t.Errorf("stalled: Confidence = (%v, %v), want (0, nil)", conf, err)
	}

	conf, err := tr.Confidence(ctx, "never-seen-sess")
	if !errors.Is(err, ErrSourceUnavailable) {
		t.Errorf("unknown: err = %v, want ErrSourceUnavailable", err)
	}
	if conf != 0 {
		t.Errorf("unknown: conf = %v, want 0", conf)
	}
}

// TestStall_GateDeniedThreeInThirtyMinutes drives RecordGateDenied with
// a frozen clock: the first two gate-denied observations for one job
// within the 30-minute window must not fire, and the third must.
func TestStall_GateDeniedThreeInThirtyMinutes(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	tr := NewProgressTracker(runtime.NewFixedClock(time.Now()), time.Minute)

	if tr.RecordGateDenied("job-1", base) {
		t.Fatal("1st denial fired, want no fire")
	}
	if tr.RecordGateDenied("job-1", base+10*time.Minute.Milliseconds()) {
		t.Fatal("2nd denial (within window) fired, want no fire")
	}
	if !tr.RecordGateDenied("job-1", base+20*time.Minute.Milliseconds()) {
		t.Fatal("3rd denial within 30 minutes did not fire, want fire")
	}
}

// TestRecordGateDeniedPrunesOutsideWindow proves the window is a real
// sliding 30 minutes, not a lifetime counter: two denials 40 minutes
// apart (one pruned) plus a third shortly after must NOT fire, because
// only two remain inside the window.
func TestRecordGateDeniedPrunesOutsideWindow(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	tr := NewProgressTracker(runtime.NewFixedClock(time.Now()), time.Minute)

	tr.RecordGateDenied("job-2", base)
	// 40 minutes later: the first denial is now outside the 30-minute
	// window and must be pruned.
	fired := tr.RecordGateDenied("job-2", base+40*time.Minute.Milliseconds())
	if fired {
		t.Fatal("2nd denial after the 1st was pruned fired, want no fire (only 1 in window)")
	}
	fired = tr.RecordGateDenied("job-2", base+45*time.Minute.Milliseconds())
	if fired {
		t.Fatal("3rd denial fired with only 2 in window, want no fire")
	}
}

func TestRecordGateDeniedDistinctJobsIndependent(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	tr := NewProgressTracker(runtime.NewFixedClock(time.Now()), time.Minute)

	tr.RecordGateDenied("job-a", base)
	tr.RecordGateDenied("job-a", base+time.Minute.Milliseconds())
	if fired := tr.RecordGateDenied("job-b", base+2*time.Minute.Milliseconds()); fired {
		t.Fatal("job-b's 1st denial fired, want job-a's count to never leak into job-b")
	}
}

func TestWatchedReportsTouchedSessions(t *testing.T) {
	tr := NewProgressTracker(runtime.NewFixedClock(time.Now()), time.Minute)
	tr.Touch("a")
	tr.Touch("b")
	got := tr.Watched()
	if len(got) != 2 {
		t.Fatalf("Watched() = %v, want 2 entries", got)
	}
}
