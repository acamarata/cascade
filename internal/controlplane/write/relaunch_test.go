package write

import (
	"testing"
	"time"
)

func TestRecommendRelaunchAfterDelay(t *testing.T) {
	now := time.Now()
	w := StartWindow(now)

	// Simulate 6 minutes elapsed
	later := now.Add(6 * time.Minute)
	rec, ok := Recommend(w, later)

	if !ok {
		t.Fatal("expected recommendation to be true after 6 minutes")
	}
	if rec.Action != "relaunch_saved_spec" {
		t.Errorf("expected Action=%q, got %q", "relaunch_saved_spec", rec.Action)
	}
}

func TestNoRecommendBeforeDelay(t *testing.T) {
	now := time.Now()
	w := StartWindow(now)

	// Simulate 1 minute elapsed (before 5-minute delay)
	later := now.Add(1 * time.Minute)
	rec, ok := Recommend(w, later)

	if ok {
		t.Errorf("expected no recommendation before delay, got %+v", rec)
	}
}

func TestNoRecommendAfterRelaunch(t *testing.T) {
	now := time.Now()
	w := StartWindow(now)
	w.Relaunched = true

	// Simulate 10 minutes elapsed
	later := now.Add(10 * time.Minute)
	rec, ok := Recommend(w, later)

	if ok {
		t.Errorf("expected no recommendation after relaunch, got %+v", rec)
	}
}

func TestSupervisorDeadTriggersWindow(t *testing.T) {
	now := time.Now()
	w := StartWindow(now)

	if !w.Active {
		t.Error("expected window to be active after StartWindow")
	}
	if w.Started != now {
		t.Errorf("expected Started=%v, got %v", now, w.Started)
	}
	if w.Relaunched {
		t.Error("expected Relaunched=false after StartWindow")
	}
}
