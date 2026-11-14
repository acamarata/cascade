//go:build !windows

package main

// Purpose: the composed inbox delivery wiring: the push path stays flag-off
// and the delivery subscriptions end with the daemon run context.
// Constraints: in-process composeDaemon over a temp HOME; no network.

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/rpc"
)

// TestPushStaysFlagOff: composeDaemon records inbox.push Skipped with the
// flag-off reason, and the delivery loop itself is running.
func TestPushStaysFlagOff(t *testing.T) {
	m, _ := composeNotify(t)
	st, ok := subsystemState(m, inboxPushSubsystem)
	if !ok {
		t.Fatal("composeDaemon recorded no inbox.push entry")
	}
	if st.State != daemon.SubsystemSkipped || st.Detail != inboxPushOffReason {
		t.Fatalf("inbox.push = %v %q, want Skipped %q", st.State, st.Detail, inboxPushOffReason)
	}
	if d, ok := subsystemState(m, inboxDeliverySubsystem); !ok || d.State != daemon.SubsystemRunning {
		t.Fatalf("inbox.delivery = %+v (found %v), want Running", d, ok)
	}
}

// TestInboxDeliveryStopsWithRunContext: a session registered after start is
// subscribed through the composed wiring; canceling the run context ends the
// loop and unsubscribes it.
func TestInboxDeliveryStopsWithRunContext(t *testing.T) {
	seen := observeWiring(t)
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	deps := newRunTestDeps(t, nonexistentExecutable(t))
	opts, cleanups, err := composeDaemon(ctx, deps, func(*rpc.Registry, *rpc.SSEMux) {})
	t.Cleanup(func() { cancel(); runCleanupsLIFO(cleanups) })
	if err != nil {
		t.Fatalf("composeDaemon: %v", err)
	}
	w := <-seen
	rec := sessions.SessionRecord{SessionID: "sess-run", Harness: "cc-harness", PID: 1, State: sessions.StateActive.String()}
	if err := sessions.New(w.Store, w.Clock, w.Bus).Upsert(ctx, rec); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	reg := w.Deps.Notify.Service().Registry
	waitForCond(t, func() bool { return registeredIDs(reg)["sess-run"] }, "sess-run subscribed")
	cancel()
	opts.Manifest.Wait()
	if ids := registeredIDs(reg); len(ids) != 0 {
		t.Fatalf("subscriptions after run context end = %v, want none", ids)
	}
}

// waitForCond polls cond until it holds or the bound passes.
func waitForCond(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
