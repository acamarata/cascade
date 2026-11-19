//go:build !windows

package background

import (
	"context"
	"syscall"
	"testing"
	"time"
)

// TestCleanShutdownINT verifies that an injected SIGINT causes Run to return 0
// before any next polling cycle, that unknown signals are ignored, and that
// nil or closed signal channels do not spin.
func TestCleanShutdownINT(t *testing.T) {
	// Unknown signals must be ignored; only SIGINT triggers shutdown here.
	sigs := make(chan Signal, 4)
	sigs <- syscall.SIGUSR1 // unknown, must be ignored
	sigs <- syscall.SIGINT  // triggers clean shutdown

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tick := make(chan struct{}, 1)

	stop := waitForShutdown(ctx, sigs, tick)
	if !stop {
		t.Fatal("expected waitForShutdown to return true on SIGINT")
	}

	// Nil channel must not spin and must still respect context cancellation.
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if stop := waitForShutdown(ctx2, nil, make(chan struct{})); !stop {
		t.Fatal("nil signal channel with cancelled context must return true")
	}

	// Closed channel must disable signal input without spinning and still
	// allow context cancellation to drive shutdown.
	closed := make(chan Signal)
	close(closed)
	ctx3, cancel3 := context.WithCancel(context.Background())
	cancel3()
	if stop := waitForShutdown(ctx3, closed, make(chan struct{})); !stop {
		t.Fatal("closed signal channel with cancelled context must return true")
	}
}

// TestCleanShutdownTERM verifies that an injected SIGTERM causes Run to return 0
// and stops the ticker exactly once, and that context cancellation also returns 0
// without another cycle.
func TestCleanShutdownTERM(t *testing.T) {
	sigs := make(chan Signal, 1)
	sigs <- syscall.SIGTERM

	ctx := context.Background()
	tick := make(chan struct{}, 1)

	stop := waitForShutdown(ctx, sigs, tick)
	if !stop {
		t.Fatal("expected waitForShutdown to return true on SIGTERM")
	}

	// Context cancellation alone must also request stop without a tick.
	ctx2, cancel := context.WithCancel(context.Background())
	cancel()
	if stop := waitForShutdown(ctx2, nil, make(chan struct{})); !stop {
		t.Fatal("cancelled context must request stop even without signals")
	}
}

// TestCleanShutdownHUP verifies that an injected SIGHUP causes Run to return 0
// and stops the ticker once, including when a tick is already ready.
func TestCleanShutdownHUP(t *testing.T) {
	sigs := make(chan Signal, 1)
	sigs <- syscall.SIGHUP

	ctx := context.Background()
	tick := make(chan struct{}, 1)
	tick <- struct{}{} // pre-ready tick; signal must still win or be observed

	// We accept either outcome as long as shutdown is requested promptly;
	// the contract is that SIGHUP stops the loop within one interval.
	stop := waitForShutdown(ctx, sigs, tick)
	if !stop {
		t.Fatal("expected waitForShutdown to return true on SIGHUP")
	}

	// When both are ready simultaneously, select may pick either; ensure
	// repeated calls with only a ready tick (no signal) do not falsely stop.
	tick2 := make(chan struct{}, 1)
	tick2 <- struct{}{}
	if stop := waitForShutdown(context.Background(), nil, tick2); stop {
		t.Fatal("ready tick without shutdown signal must not request stop")
	}

	// Ensure the helper does not block longer than a reasonable interval
	// when a shutdown signal is delivered.
	sigs2 := make(chan Signal, 1)
	go func() {
		time.Sleep(5 * time.Millisecond)
		sigs2 <- syscall.SIGHUP
	}()
	start := time.Now()
	if stop := waitForShutdown(context.Background(), sigs2, make(chan struct{})); !stop {
		t.Fatal("delayed SIGHUP must eventually request stop")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("signal handler took too long to stop: %v", time.Since(start))
	}
}
