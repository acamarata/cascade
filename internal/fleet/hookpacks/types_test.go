// Purpose: the HookEventType closed-set fail-closed lookup, SessionsPack's
//
//	descriptor set, and HookPayload's wire allowlist shape (exactly six
//	fields, nothing else survives a marshal round trip).
//
// SPORT: fleet/hookpacks (ADD, per T-4 sport_updates).
package hookpacks_test

import (
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/hookpacks"
)

func TestHookPack_SessionsPackHasFixtureBackedDescriptorsOnly(t *testing.T) {
	pack := hookpacks.SessionsPack()
	if pack.Name != "sessions" {
		t.Fatalf("Name = %q, want %q", pack.Name, "sessions")
	}
	want := map[hookpacks.HookEventType]bool{
		hookpacks.EventPreToolUse:  true,
		hookpacks.EventPostToolUse: true,
		hookpacks.EventStop:        true,
	}
	if len(pack.Descriptors) != len(want) {
		t.Fatalf("Descriptors = %+v, want exactly the fixture-backed types %v", pack.Descriptors, want)
	}
	for _, d := range pack.Descriptors {
		if !want[d.EventType] {
			t.Fatalf("unexpected descriptor event type %q: no captured fixture backs it", d.EventType)
		}
	}
}

func TestHookPack_ParseHookEventTypeFailsClosedOnUnknown(t *testing.T) {
	if !hookpacks.ParseHookEventType(hookpacks.EventStop) {
		t.Fatal("ParseHookEventType(EventStop) = false, want true")
	}
	if hookpacks.ParseHookEventType(hookpacks.HookEventType("TotallyMadeUp")) {
		t.Fatal("ParseHookEventType(\"TotallyMadeUp\") = true, want false (fail closed)")
	}
	if hookpacks.ParseHookEventType("") {
		t.Fatal("ParseHookEventType(\"\") = true, want false")
	}
}

// TestHookPayload_WireShapeIsExactlySixFields proves the allowlist is
// structural: encoding a fully-populated HookPayload and decoding it
// back into a bare map yields exactly these six keys, never more —
// there is no field on the Go struct through which any other value
// could ever be marshaled out.
func TestHookPayload_WireShapeIsExactlySixFields(t *testing.T) {
	p := hookpacks.HookPayload{
		Harness: "test-harness", EventType: hookpacks.EventPreToolUse,
		SessionID: "s1", PID: 7, Account: "acct", TimestampMs: 1_700_000_000_000,
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var asMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	wantKeys := []string{"harness", "event_type", "session_id", "pid", "account", "timestamp_ms"}
	if len(asMap) != len(wantKeys) {
		t.Fatalf("marshaled keys = %v, want exactly %v", keysOf(asMap), wantKeys)
	}
	for _, k := range wantKeys {
		if _, ok := asMap[k]; !ok {
			t.Fatalf("missing expected key %q in %v", k, keysOf(asMap))
		}
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
