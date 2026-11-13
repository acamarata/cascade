package log

import (
	"errors"
	"testing"
)

// fakeStarter is an injected fake process starter for tests.
type fakeStarter struct {
	called bool
}

func (f *fakeStarter) Start(runID string, tickets []string, logPath string) (int, error) {
	f.called = true
	return 0, nil
}

// TestFlowRunLogPathFormat verifies that a valid runID produces the correct log path
// and that invalid runIDs return ErrInvalidRunID without calling Starter.
func TestFlowRunLogPathFormat(t *testing.T) {
	t.Run("valid runID r7", func(t *testing.T) {
		fs := &fakeStarter{}
		ri, err := FlowRun(fs, "r7", []string{"T1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "data/gc-flow/r7.log"
		if ri.LogPath != want {
			t.Errorf("LogPath = %q, want %q", ri.LogPath, want)
		}
		if ri.RunID != "r7" {
			t.Errorf("RunID = %q, want %q", ri.RunID, "r7")
		}
		if fs.called {
			t.Error("Start should not be called yet")
		}
	})

	invalidIDs := []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"starts with hyphen", "-abc"},
		{"starts with underscore", "_abc"},
		{"contains slash", "a/b"},
		{"path traversal", "../etc"},
		{"overlong", "A1234567890123456789012345678901234567890123456789012345678901234"},
		{"space in id", "a b"},
	}
	for _, tc := range invalidIDs {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeStarter{}
			ri, err := FlowRun(fs, tc.id, []string{"T1"})
			if !errors.Is(err, ErrInvalidRunID) {
				t.Errorf("err = %v, want ErrInvalidRunID", err)
			}
			if ri != (RunInfo{}) {
				t.Errorf("expected zero RunInfo, got %+v", ri)
			}
			if fs.called {
				t.Error("Start must not be called for invalid runID")
			}
		})
	}
}

// TestFlowRunEmptyPlanFails verifies validation of starter, plan, and tickets.
func TestFlowRunEmptyPlanFails(t *testing.T) {
	t.Run("nil starter", func(t *testing.T) {
		ri, err := FlowRun(nil, "r1", []string{"T1"})
		if !errors.Is(err, ErrInvalidStarter) {
			t.Errorf("err = %v, want ErrInvalidStarter", err)
		}
		if ri != (RunInfo{}) {
			t.Errorf("expected zero RunInfo, got %+v", ri)
		}
	})

	t.Run("typed nil starter", func(t *testing.T) {
		var fs *fakeStarter
		ri, err := FlowRun(fs, "r1", []string{"T1"})
		if !errors.Is(err, ErrInvalidStarter) {
			t.Errorf("err = %v, want ErrInvalidStarter", err)
		}
		if ri != (RunInfo{}) {
			t.Errorf("expected zero RunInfo, got %+v", ri)
		}
	})

	t.Run("nil tickets", func(t *testing.T) {
		fs := &fakeStarter{}
		ri, err := FlowRun(fs, "r1", nil)
		if !errors.Is(err, ErrEmptyPlan) {
			t.Errorf("err = %v, want ErrEmptyPlan", err)
		}
		if err.Error() != "empty plan" {
			t.Errorf("error text = %q, want %q", err.Error(), "empty plan")
		}
		if ri != (RunInfo{}) {
			t.Errorf("expected zero RunInfo, got %+v", ri)
		}
		if fs.called {
			t.Error("Start must not be called")
		}
	})

	t.Run("empty tickets", func(t *testing.T) {
		fs := &fakeStarter{}
		ri, err := FlowRun(fs, "r1", []string{})
		if !errors.Is(err, ErrEmptyPlan) {
			t.Errorf("err = %v, want ErrEmptyPlan", err)
		}
		if err.Error() != "empty plan" {
			t.Errorf("error text = %q, want %q", err.Error(), "empty plan")
		}
		if ri != (RunInfo{}) {
			t.Errorf("expected zero RunInfo, got %+v", ri)
		}
		if fs.called {
			t.Error("Start must not be called")
		}
	})

	t.Run("empty ticket entry", func(t *testing.T) {
		fs := &fakeStarter{}
		ri, err := FlowRun(fs, "r1", []string{""})
		if !errors.Is(err, ErrInvalidTicket) {
			t.Errorf("err = %v, want ErrInvalidTicket", err)
		}
		if ri != (RunInfo{}) {
			t.Errorf("expected zero RunInfo, got %+v", ri)
		}
		if fs.called {
			t.Error("Start must not be called")
		}
	})

	t.Run("whitespace ticket entry", func(t *testing.T) {
		fs := &fakeStarter{}
		ri, err := FlowRun(fs, "r1", []string{"  "})
		if !errors.Is(err, ErrInvalidTicket) {
			t.Errorf("err = %v, want ErrInvalidTicket", err)
		}
		if ri != (RunInfo{}) {
			t.Errorf("expected zero RunInfo, got %+v", ri)
		}
		if fs.called {
			t.Error("Start must not be called")
		}
	})

	t.Run("duplicate ticket entries", func(t *testing.T) {
		fs := &fakeStarter{}
		ri, err := FlowRun(fs, "r1", []string{"T1", "T1"})
		if !errors.Is(err, ErrInvalidTicket) {
			t.Errorf("err = %v, want ErrInvalidTicket", err)
		}
		if ri != (RunInfo{}) {
			t.Errorf("expected zero RunInfo, got %+v", ri)
		}
		if fs.called {
			t.Error("Start must not be called")
		}
	})
}
