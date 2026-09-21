package nodes

// Purpose (this file): RunHeartbeatLoop under a manually fired ticker.
//   Split from heartbeat_test.go, which sits at Art.10.3's 300-line cap.
//
// SPORT: nodes.heartbeat-loop (TEST) — split 2026-09-21.

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeTicker is a manually-fired Ticker for deterministic loop tests.
type fakeTicker struct {
	ch      chan struct{}
	stopped bool
}

func newFakeTicker() *fakeTicker         { return &fakeTicker{ch: make(chan struct{}, 1)} }
func (f *fakeTicker) C() <-chan struct{} { return f.ch }
func (f *fakeTicker) Stop()              { f.stopped = true }
func (f *fakeTicker) fire()              { f.ch <- struct{}{} }

func TestRunHeartbeatLoopSendsOnTick(t *testing.T) {
	id := testIdentity(t, "loop")
	ks := storedTestKeystore(t, "loop", id.NodeID)

	ticker := newFakeTicker()
	sent := make(chan HeartbeatFrame, 4)
	// loopErr captures what the loop would otherwise drop: a frame-build
	// failure makes the loop `continue` without sending, and with no
	// OnError that is indistinguishable from slowness at the timeout
	// below. CI run 35545822346 failed here with only "timed out", which
	// is exactly the ambiguity this closes.
	loopErr := make(chan error, 4)
	var seq uint64
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunHeartbeatLoop(ctx, HeartbeatLoopOptions{
			Ticker: ticker,
			NextSequence: func() uint64 {
				seq++
				return seq
			},
			Send: func(_ context.Context, f HeartbeatFrame) error {
				sent <- f
				return nil
			},
			OnError:      func(err error) { loopErr <- err },
			BuildReport:  validReport,
			Keystore:     ks,
			NodeID:       id.NodeID,
			EnrollmentID: "enr-1",
		})
		close(done)
	}()

	ticker.fire()
	// The tick itself cannot be lost (the fake ticker's channel is
	// buffered), so what this waits on is real work: a report build and
	// an Ed25519 signature through the keystore. Under -race on a loaded
	// CI runner that took over six seconds once; the budget only matters
	// on failure, so it is generous rather than tight.
	select {
	case f := <-sent:
		if f.Sequence != 1 {
			t.Fatalf("got sequence %d, want 1", f.Sequence)
		}
	case err := <-loopErr:
		t.Fatalf("heartbeat loop reported an error instead of sending: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for heartbeat send")
	}
	cancel()
	<-done
	if !ticker.stopped {
		t.Fatal("expected Ticker.Stop() to be called on loop exit")
	}
}

func TestRunHeartbeatLoopReportsSendErrorAndContinues(t *testing.T) {
	id := testIdentity(t, "loop2")
	ks := storedTestKeystore(t, "loop2", id.NodeID)

	ticker := newFakeTicker()
	errs := make(chan error, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunHeartbeatLoop(ctx, HeartbeatLoopOptions{
			Ticker:       ticker,
			NextSequence: func() uint64 { return 1 },
			Send: func(context.Context, HeartbeatFrame) error {
				return cascade.New(cascade.KindUnavailable, "controller unreachable")
			},
			BuildReport:  validReport,
			Keystore:     ks,
			NodeID:       id.NodeID,
			EnrollmentID: "enr-1",
			OnError:      func(err error) { errs <- err },
		})
		close(done)
	}()
	ticker.fire()
	select {
	case err := <-errs:
		if err == nil {
			t.Fatal("expected a non-nil send error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OnError")
	}
	cancel()
	<-done
}
