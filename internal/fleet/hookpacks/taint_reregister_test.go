// Purpose: preserve session taint through harness session re-registration.
package hookpacks_test

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
)

func TestTaintSurvivesReRegistration(t *testing.T) {
	ctx := context.Background()
	registry, store, clock := newHandlerFixture(t, nil)
	sendHook(t, registry, "s1", hookpacks.EventSessionStart, clock.Now().UnixMilli())
	if err := store.MarkUntrusted(ctx, "s1", "inbox:1"); err != nil {
		t.Fatal(err)
	}
	before, err := store.Taint(ctx, "s1")
	if err != nil || !before.Tainted || before.SinceMs != clock.Now().UnixMilli() || before.ProvenanceRef != "inbox:1" {
		t.Fatalf("initial taint=%+v, err=%v", before, err)
	}
	for _, step := range []struct {
		name  string
		event hookpacks.HookEventType
	}{
		{"compaction", hookpacks.EventSessionStart},
		{"resume", hookpacks.EventSessionStart},
		{"subagent", hookpacks.EventSubagentStart},
	} {
		t.Run(step.name, func(t *testing.T) {
			clock.Advance(time.Second)
			sendHook(t, registry, "s1", step.event, clock.Now().UnixMilli())
			after, err := store.Taint(ctx, "s1")
			if err != nil || after != before {
				t.Fatalf("taint after %s=%+v, want %+v, err=%v", step.name, after, before, err)
			}
			policy := &fakePolicy{allowed: false}
			params := hookpacks.ToolGateParams{SessionID: "s1", ToolName: "Bash", HookEventName: string(hookpacks.EventPreToolUse)}
			result, err := hookpacks.DecideToolGate(ctx, params, "claude-code", store, policy)
			if err != nil || result.Allow || policy.calls != 1 {
				t.Fatalf("gate after %s=%+v, err=%v, policy calls=%d", step.name, result, err, policy.calls)
			}
		})
	}
}
