//go:build !windows

// Purpose: prove the monitor, respawn and host-call consumers are bound to
//
//	the Handle's lifetime, driven by fake Commanders with start counters:
//	a cancelled lifetime ends a backoff at once and starts nothing, Close
//	never respawns, and every live transport has exactly one consumer.
//
// Inputs: fakeCommander (runtime_test.go), capabilitySpy (lifetime_test.go).
// Outputs: test verdicts only.
// Constraints: split from restart_test.go, which is untagged and builds on
//
//	windows, where Launch and these fakes do not exist.
//
// SPORT: internal/plugins/process lifetime (TEST) — P1-PLG-09.

package process

import (
	"bytes"
	"context"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/hooks/egress"
)

// fakeRuntime builds a runtime whose every start is a fresh fake from mk.
func fakeRuntime(policy RestartPolicy, audit AuditSink, mk func(n int32) Commander) (*ProcessRuntime, *int32) {
	var starts int32
	rt := &ProcessRuntime{
		Stderr: &bytes.Buffer{}, Registrar: egressRegistryAdapter{reg: egress.NewRegistry()},
		Restart: policy, Audit: audit,
		commandFactory: func(context.Context, Manifest) Commander { return mk(atomic.AddInt32(&starts, 1)) },
	}
	return rt, &starts
}

// awaitTrue polls cond for up to 5s.
func awaitTrue(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestMonitorBackoffCancelledByLifetime(t *testing.T) {
	audit := &fakeAuditSink{}
	rt, starts := fakeRuntime(RestartPolicy{MaxAttempts: 3, InitialBackoff: 10 * time.Second}, audit,
		func(int32) Commander { return newFakeCommander("1.0.0", 7, true, nil) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := rt.Launch(ctx, trustedManifest("backoff-demo"))
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	awaitTrue(t, "the first crash report (the monitor is entering its 10s backoff)", func() bool { return len(audit.snapshot()) == 1 })

	cancel()
	if !waitFor(context.Background(), h.monitorDone, 100*time.Millisecond) {
		t.Fatal("monitor still running 100ms after its lifetime was cancelled mid-backoff")
	}
	time.Sleep(50 * time.Millisecond)
	if n := atomic.LoadInt32(starts); n != 1 {
		t.Fatalf("starts = %d after the lifetime ended during backoff, want 1 (no respawn)", n)
	}
	if n := len(audit.snapshot()); n != 1 {
		t.Fatalf("crash reports = %d, want exactly one before lifetime cancellation", n)
	}
}

func TestRespawnStopsWhenLifetimeEnds(t *testing.T) {
	rt, starts := fakeRuntime(RestartPolicy{MaxAttempts: 3, InitialBackoff: time.Millisecond}, nil,
		func(int32) Commander { return newFakeCommander("1.0.0", 0, false, nil) })
	ctx, cancel := context.WithCancel(context.Background())
	h, err := rt.Launch(ctx, trustedManifest("respawn-demo"))
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() { _ = h.Close(context.Background()) })

	cancel()
	start := time.Now()
	cmd, reaped, err := rt.respawn(h.Manifest, h)
	if err == nil || cmd != nil || reaped != nil {
		t.Fatalf("respawn after the lifetime ended = (%v, %v, %v), want a refusal", cmd, reaped, err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("respawn took %v to refuse an ended lifetime, want < 100ms", elapsed)
	}
	if n := atomic.LoadInt32(starts); n != 1 {
		t.Fatalf("starts = %d, want 1: respawn started a process after the lifetime ended", n)
	}
}

func TestHandleCloseNeverRespawns(t *testing.T) {
	for _, tc := range []struct {
		name  string
		crash bool // true: Close lands while the monitor is in its backoff
	}{{"running", false}, {"in-backoff", true}} {
		t.Run(tc.name, func(t *testing.T) {
			rt, starts := fakeRuntime(RestartPolicy{MaxAttempts: 5, InitialBackoff: 200 * time.Millisecond}, nil,
				func(int32) Commander { return newFakeCommander("1.0.0", 1, tc.crash, nil) })
			h, err := rt.Launch(context.Background(), trustedManifest("close-demo"))
			if err != nil {
				t.Fatalf("Launch: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := h.Close(ctx); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if !closed(h.monitorDone) {
				t.Fatal("Close returned while the monitor was still running")
			}
			time.Sleep(400 * time.Millisecond) // two backoffs' worth
			if n := atomic.LoadInt32(starts); n != 1 {
				t.Fatalf("starts = %d after Close, want 1 (the monitor respawned)", n)
			}
		})
	}
}

// consumerGoroutines counts goroutines currently inside consumeHostCalls.
func consumerGoroutines() int {
	buf := make([]byte, 1<<22)
	n := runtime.Stack(buf, true)
	return strings.Count(string(buf[:n]), "process.(*ProcessRuntime).consumeHostCalls(")
}

func TestOneHostCallConsumerAfterRestart(t *testing.T) {
	spy := capabilitySpy{seen: make(chan string, 8)}
	var third atomic.Pointer[fakeCommander]
	rt, starts := fakeRuntime(RestartPolicy{MaxAttempts: 5, InitialBackoff: time.Millisecond}, nil, func(n int32) Commander {
		f := newFakeCommander("1.0.0", 9, n < 3, nil) // the first two crash right after the handshake
		if n == 3 {
			third.Store(f)
		}
		return f
	})
	rt.CapabilityChecker = spy
	baseConsumers, baseGoroutines := consumerGoroutines(), runtime.NumGoroutine()
	h, err := rt.Launch(context.Background(), trustedManifest("consumer-demo"))
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() { _ = h.Close(context.Background()) })

	awaitTrue(t, "two forced restarts (third process live)", func() bool { return atomic.LoadInt32(starts) == 3 && h.Alive() })
	awaitTrue(t, "exactly one consumer goroutine beyond the baseline", func() bool { return consumerGoroutines() == baseConsumers+1 })
	// One live plugin: its fake's responder, the transport's read loop,
	// the monitor and the consumer. Earlier processes left nothing behind.
	awaitTrue(t, "no goroutine growth from the two dead processes", func() bool { return runtime.NumGoroutine()-baseGoroutines <= 4 })

	frame := `{"jsonrpc":"2.0","method":"host_secret_ref","params":{"key":"counted-once"}}` + "\n"
	if _, err := third.Load().stdoutW.Write([]byte(frame)); err != nil {
		t.Fatalf("writing host frame: %v", err)
	}
	select {
	case key := <-spy.seen:
		if key != "counted-once" {
			t.Fatalf("host call key = %q, want counted-once", key)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the live transport's host frame reached no consumer")
	}
	select {
	case key := <-spy.seen:
		t.Fatalf("host frame checked twice (second key %q)", key)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestRespawnLosingTheRaceIsKilled: the lifetime ends while a respawned
// child is starting; respawn kills and reaps that child (no grace) and
// installs nothing, so Close's view of the current child stays valid.
func TestRespawnLosingTheRaceIsKilled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var killed atomic.Bool
	rt, starts := fakeRuntime(RestartPolicy{MaxAttempts: 3, InitialBackoff: time.Millisecond}, nil, func(n int32) Commander {
		f := newFakeCommander("1.0.0", 0, false, nil)
		if n == 1 {
			return f
		}
		cancel() // the lifetime ends as the respawned child starts
		return raceCommander{fakeCommander: f, killed: &killed}
	})
	h, err := rt.Launch(ctx, trustedManifest("race-demo"))
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	first := h.cmd
	if _, _, err := rt.respawn(h.Manifest, h); err == nil {
		t.Fatal("respawn installed a child after its lifetime ended")
	}
	if !killed.Load() || atomic.LoadInt32(starts) != 2 {
		t.Fatalf("losing child killed=%v starts=%d, want killed and 2 starts", killed.Load(), atomic.LoadInt32(starts))
	}
	if h.cmd != first {
		t.Fatal("respawn replaced the current child after losing the race")
	}
}

// raceCommander records the SIGKILL and exits on it.
type raceCommander struct {
	*fakeCommander
	killed *atomic.Bool
}

func (r raceCommander) Signal(sig os.Signal) error {
	if sig == syscall.SIGKILL {
		r.killed.Store(true)
		r.exit()
	}
	return nil
}

func TestLifetimeFallbacks(t *testing.T) {
	if (&Handle{}).lifetime() != context.Background() {
		t.Fatal("a Handle literal's lifetime must be context.Background, never nil")
	}
	owner, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &Handle{lifetimeCtx: owner}
	if h.lifetime() != owner || h.childCtx() != owner {
		t.Fatal("without a run context, lifetime() and childCtx() are the caller's lifetime")
	}
}
