package update

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestDrainBranches(t *testing.T) {
	if _, err := Drain(context.Background(), DrainDeps{}); !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("empty deps = %v", err)
	}
	noSignal := func(int) error { t.Fatal("signalled"); return nil }
	off := DrainDeps{Status: func(context.Context) (bool, int, error) { return false, 0, nil }, Signal: noSignal}
	if s, err := Drain(context.Background(), off); s || err != nil {
		t.Fatalf("not running = %v, %v", s, err)
	}
	broken := DrainDeps{Status: func(context.Context) (bool, int, error) { return false, 0, errors.New("pidfile") }, Signal: noSignal}
	if _, err := Drain(context.Background(), broken); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("status error = %v", err)
	}
	failing := DrainDeps{Status: func(context.Context) (bool, int, error) { return true, 9, nil }, Signal: func(int) error { return errors.New("esrch") }}
	if _, err := Drain(context.Background(), failing); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("signal error = %v", err)
	}
}

func TestWaitVersionBranches(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	if err := WaitVersion(context.Background(), DrainDeps{}, "2.0.1"); !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("empty deps = %v", err)
	}
	ok := DrainDeps{Clock: clock, Version: func(context.Context) (string, error) { return "v2.0.1", nil }}
	if err := WaitVersion(context.Background(), ok, "2.0.1"); err != nil {
		t.Fatalf("matching version = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stuck := DrainDeps{Clock: clock, Poll: time.Hour, Version: func(context.Context) (string, error) { return "", errors.New("down") }}
	if err := WaitVersion(ctx, stuck, "2.0.1"); !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("canceled = %v", err)
	}
}

// TestWaitVersionBoundsEachProbe: every probe runs under a deadline no later
// than the remaining timeout, and an answer that arrives after the deadline
// (by the injected clock) is not accepted.
func TestWaitVersionBoundsEachProbe(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	var bounded []bool
	late := DrainDeps{Clock: clock, Poll: time.Millisecond, Timeout: 30 * time.Second,
		Version: func(ctx context.Context) (string, error) {
			dl, ok := ctx.Deadline()
			bounded = append(bounded, ok && time.Until(dl) <= 30*time.Second)
			clock.Advance(31 * time.Second)
			return "2.0.1", nil
		}}
	err := WaitVersion(context.Background(), late, "2.0.1")
	wantKind(t, err, cascade.KindUnavailable, "did not come back on version 2.0.1")
	if len(bounded) != 1 || !bounded[0] {
		t.Fatalf("probes = %v, want one probe bounded by the remaining timeout", bounded)
	}
	clock2 := runtime.NewFixedClock(time.Unix(0, 0))
	var remaining []time.Duration
	slow := DrainDeps{Clock: clock2, Poll: time.Millisecond, Timeout: 10 * time.Second,
		Version: func(ctx context.Context) (string, error) {
			dl, ok := ctx.Deadline()
			if !ok {
				t.Error("a probe ran without a deadline")
			}
			remaining = append(remaining, time.Until(dl))
			clock2.Advance(4 * time.Second)
			return "2.0.0", nil
		}}
	err = WaitVersion(context.Background(), slow, "2.0.1")
	wantKind(t, err, cascade.KindUnavailable, "`cascade daemon start`")
	if len(remaining) != 3 || remaining[1] > 6*time.Second || remaining[2] > 2*time.Second {
		t.Fatalf("probe bounds = %v, want 3 probes bounded by 10s, 6s, 2s", remaining)
	}
}
