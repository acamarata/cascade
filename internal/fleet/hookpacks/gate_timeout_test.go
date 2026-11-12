// Purpose: ensure harness gate timeouts leave room for client deadlines.
package hookpacks_test

import (
	"encoding/json"
	"runtime"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
)

func TestGateHookTimeoutExceedsDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the tool-gate hook command is a POSIX shell line; Windows hook packs are tier-2")
	}
	cases := []struct {
		pack     hookpacks.HookPack
		deadline int
		timeout  int
		events   []hookpacks.HookEventType
	}{
		{hookpacks.ToolGatePack(), hookpacks.ToolGateClientDeadline, hookpacks.ToolGateHookTimeoutSeconds,
			[]hookpacks.HookEventType{hookpacks.EventPreToolUse}},
		{completionPack(t), int(hookpacks.CompletionClientBudget() / time.Second), hookpacks.CompletionGateHookTimeoutSeconds,
			[]hookpacks.HookEventType{hookpacks.EventTaskCompleted, hookpacks.EventStop}},
	}
	for _, tc := range cases {
		t.Run(tc.pack.Name, func(t *testing.T) {
			rendered, err := tc.pack.RenderWithBinary("/tmp/s.sock", "/opt/cas cade/bin/cascade")
			if err != nil {
				t.Fatal(err)
			}
			if len(rendered) != len(tc.events) {
				t.Fatalf("got %d entries, want %d", len(rendered), len(tc.events))
			}
			for i, raw := range rendered {
				if tc.pack.Descriptors[i].EventType != tc.events[i] {
					t.Fatalf("unexpected event: %s", tc.pack.Descriptors[i].EventType)
				}
				var entry struct {
					Hooks []struct{ Timeout int } `json:"hooks"`
				}
				if err := json.Unmarshal(raw, &entry); err != nil || len(entry.Hooks) != 1 {
					t.Fatalf("decode entry: %v, hooks=%d", err, len(entry.Hooks))
				}
				if got := entry.Hooks[0].Timeout; got < tc.deadline+20 || got != tc.timeout {
					t.Errorf("%s timeout=%d, want %d and >= deadline %d + 20", tc.events[i], got, tc.timeout, tc.deadline)
				}
			}
		})
	}
}
