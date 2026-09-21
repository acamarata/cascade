package install

// Purpose (this file): the round-2 rework proof for T0 decision D4 --
//   RunRequest.ThreadID must reach EVERY event a run publishes -- split
//   out of flow_test.go under the 300-line cap.
// SPORT: plugins/cascade-pa/install (TEST) -- FIX P1-E24-W5-S50-T4 (D4).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
)

// TestConversationalInstall_ThreadIDPropagatesToEveryEvent proves
// RunRequest.ThreadID reaches every event a run publishes, not just the
// first or last -- a real EventBus's CLIENT-LOCAL ECHO (internal/plugins'
// chatEchoPublisher) appends every one of them to the SAME conversation
// thread.
func TestConversationalInstall_ThreadIDPropagatesToEveryEvent(t *testing.T) {
	const wantThread = "thread-xyz789"
	cand := newCandidate("git-tools", plugin.RuntimeBuiltin)
	f, d := newDeps(cand)

	result, err := f.Run(context.Background(), RunRequest{Intent: "git push", ThreadID: wantThread})
	if err != nil || !result.Resumed {
		t.Fatalf("Run() = (%+v, %v), want resumed with no error", result, err)
	}
	if len(d.bus.events) == 0 {
		t.Fatal("no events published")
	}
	for _, evt := range d.bus.events {
		if evt.ThreadID != wantThread {
			t.Errorf("event %v ThreadID = %q, want %q on every event this run publishes", evt.Kind, evt.ThreadID, wantThread)
		}
	}
}

// TestConversationalInstall_EmptyThreadIDPropagatesAsEmpty proves a run
// with no originating thread still runs to completion and publishes
// events carrying the empty ThreadID (never a fabricated one) -- the
// EventBus/echo layer is what decides to skip the echo on empty, not Flow
// itself (cascadepa_install_echo.go's own contract).
//
// REWORK (round-3, T0 decision D3, FLAG 5): round-2's loop below had no
// non-empty guard -- if the run had published nothing at all, the loop
// would range over zero events and pass vacuously, proving nothing. Its
// sibling (TestConversationalInstall_ThreadIDPropagatesToEveryEvent)
// already guards this; this test now does too.
func TestConversationalInstall_EmptyThreadIDPropagatesAsEmpty(t *testing.T) {
	cand := newCandidate("git-tools", plugin.RuntimeBuiltin)
	f, d := newDeps(cand)

	result, err := f.Run(context.Background(), RunRequest{Intent: "git push"})
	if err != nil || !result.Resumed {
		t.Fatalf("Run() = (%+v, %v), want resumed with no error", result, err)
	}
	if len(d.bus.events) == 0 {
		t.Fatal("no events published")
	}
	for _, evt := range d.bus.events {
		if evt.ThreadID != "" {
			t.Errorf("event %v ThreadID = %q, want empty when RunRequest named no thread", evt.Kind, evt.ThreadID)
		}
	}
}
