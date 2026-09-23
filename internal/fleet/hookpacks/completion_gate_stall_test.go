// Purpose: D2's own required proof -- subscribes on the REAL bus (never
// Replay, which only proves the payload round-trips) at the exact
// namespace/cursor shape internal/fleet/supervision/stall.go's
// Detector.Run itself subscribes with, and asserts both EventGateDenied
// and EventGateTimeout arrive on it from the SAME production
// RegisterCompletionCheckHandler dispatch path the daemon uses. Split
// from completion_gate_test.go to keep that file's own 300-line cap.
// SPORT: fleet/hookpacks.EventGateDenied/EventGateTimeout/ADD (P1-E32-W6-S66-T1).
package hookpacks_test

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
)

// TestCompletionHook_StallDetectorObservesBothGateEventKinds proves a
// hook-level denial with no gate call at all (unknown job id) publishes
// EventGateDenied, and a gate call that blocks past the deadline
// publishes EventGateTimeout, both observable on namespace "jobs.gate" --
// the exact subscription internal/fleet/supervision's stall detector
// makes for its R-16.73 3-in-30-minute escalation rule.
func TestCompletionHook_StallDetectorObservesBothGateEventKinds(t *testing.T) {
	registry, bus, _ := newCompletionFixtureTimeout(t, fakeGate{delay: true}, fakeResolver{jobs: map[string]bool{"job-1": true}})
	sub, err := bus.Subscribe(context.Background(), "jobs.gate", "test-stall-cursor", 8)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	dispatchCompletion(t, registry, hookpacks.CompletionHookPayload{EventType: hookpacks.EventStop, SessionID: "s1", JobID: "ghost-job"})
	dispatchCompletion(t, registry, hookpacks.CompletionHookPayload{EventType: hookpacks.EventStop, SessionID: "s1", JobID: "job-1"})

	var gotDenied, gotTimeout bool
	for i := 0; i < 2; i++ {
		select {
		case ev := <-sub.Events:
			matched := false
			if ev.Kind == hookpacks.EventGateDenied {
				gotDenied = true
				matched = true
			}
			if ev.Kind == hookpacks.EventGateTimeout {
				gotTimeout = true
				matched = true
			}
			if !matched {
				t.Fatalf("unexpected event kind %q on jobs.gate", ev.Kind)
			}
		case subErr := <-sub.Errs:
			t.Fatalf("subscription error: %v", subErr)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for both gate events on the real bus")
		}
	}
	if !gotDenied || !gotTimeout {
		t.Fatalf("observed denied=%v timeout=%v, want both", gotDenied, gotTimeout)
	}
}

// TestCompletionHook_StopHookActiveNoRecursion: StopHookActive=true still
// denies, and CompletionCheck runs exactly once (no recursion). Shell
// half: TestCompletionHookCommand_StopHookActiveNeverBypassesFailClosed.
func TestCompletionHook_StopHookActiveNoRecursion(t *testing.T) {
	calls := 0
	gate := countingGate{fakeGate{ok: false, reason: "missing evidence: build"}, &calls}
	registry, _, _ := newCompletionFixture(t, gate, fakeResolver{jobs: map[string]bool{"job-1": true}})
	resp := dispatchCompletion(t, registry, hookpacks.CompletionHookPayload{EventType: hookpacks.EventStop, JobID: "job-1", StopHookActive: true})
	if !resp.Deny || resp.Reason != "missing evidence: build" {
		t.Fatalf("StopHookActive=true deny = %+v, want Deny=true", resp)
	}
	if calls != 1 {
		t.Fatalf("CompletionCheck called %d times, want 1 (no recursion)", calls)
	}
}
