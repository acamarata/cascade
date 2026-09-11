package process

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestRestartPolicyBackoffFor(t *testing.T) {
	p := RestartPolicy{MaxAttempts: 5, InitialBackoff: time.Second}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	for i, w := range want {
		if got := p.backoffFor(i + 1); got != w {
			t.Errorf("backoffFor(%d) = %v, want %v", i+1, got, w)
		}
	}
}

func TestRestartPolicyResolvedDefaults(t *testing.T) {
	if got := (RestartPolicy{}).resolved(); got != DefaultRestartPolicy {
		t.Errorf("zero policy resolved to %+v, want %+v", got, DefaultRestartPolicy)
	}
	custom := RestartPolicy{MaxAttempts: 9}
	resolved := custom.resolved()
	if resolved.MaxAttempts != 9 || resolved.InitialBackoff != DefaultRestartPolicy.InitialBackoff {
		t.Errorf("partial policy resolved to %+v", resolved)
	}
}

func TestStateBoxTransitions(t *testing.T) {
	s := &stateBox{}
	if s.get() != stateRunning {
		t.Fatal("new stateBox should start running")
	}
	if got := s.incrementRestart(); got != 1 {
		t.Fatalf("incrementRestart = %d, want 1", got)
	}
	s.markInvalid()
	if s.get() != stateInvalid {
		t.Fatal("expected stateInvalid after markInvalid")
	}
}

func TestStderrTailerEviction(t *testing.T) {
	tail := newStderrTailer(3)
	for _, l := range []string{"a", "b", "c", "d", "e"} {
		tail.add(l)
	}
	got := tail.snapshot()
	want := []string{"c", "d", "e"}
	if len(got) != len(want) {
		t.Fatalf("snapshot = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("snapshot = %v, want %v", got, want)
		}
	}
}

func TestHandleCallRefusedWhenInvalid(t *testing.T) {
	h := &Handle{Manifest: Manifest{Name: "demo"}, state: &stateBox{}, tail: newStderrTailer(1)}
	h.state.markInvalid()
	_, err := h.Call(context.Background(), "x", nil)
	if err == nil {
		t.Fatal("expected an error from a state-invalid handle")
	}
	if !errors.Is(err, ErrPluginUnavailable) {
		t.Fatalf("errors.Is(err, ErrPluginUnavailable) = false, got %v", err)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("expected KindUnavailable, got %v", err)
	}
	if h.State() != "invalid" {
		t.Fatalf("State() = %q, want invalid", h.State())
	}
}

func TestWaitExitCodeSuccess(t *testing.T) {
	if got := waitExitCode(waiterFunc(func() error { return nil })); got != 0 {
		t.Errorf("waitExitCode(success) = %d, want 0", got)
	}
}

func TestWaitExitCodeNoCoder(t *testing.T) {
	if got := waitExitCode(waiterFunc(func() error { return errors.New("boom") })); got != -1 {
		t.Errorf("waitExitCode(no exit coder) = %d, want -1", got)
	}
}

// waiterFunc adapts a func into a Waiter for table-style tests.
type waiterFunc func() error

func (f waiterFunc) Wait() error { return f() }

func TestBackoffSleepRespectsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	backoffSleep(ctx, time.Hour)
	if time.Since(start) > time.Second {
		t.Fatalf("backoffSleep did not return promptly on ctx cancellation")
	}
}
