package tools

// Purpose (this file): fix18's two cascade_cpa_send obligations toward the
//   chat-bridge mirror — (K2) the turn's RESOLVED sensitivity, the same value
//   the tool echoes, is what reaches AppendRequest; (L3) the mirror's outcome
//   reaches the tool caller as a typed field, never a silent success.
//
// SPORT: plugins/cascade-pa:mcp-tools:cascade_cpa_send (TEST) — fix18.

import (
	"strings"
	"testing"
)

// TestSendCarriesTheResolvedSensitivity pins what the mirror is handed: an
// unset or unknown tier arrives as restricted, a named tier arrives as
// itself. Hardcoding AppendRequest.Sensitivity to "public" turns every
// narrower case RED.
func TestSendCarriesTheResolvedSensitivity(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"content":"x","thread_id":"t"}`, SensitivityRestricted},
		{`{"content":"x","thread_id":"t","sensitivity":"Public"}`, SensitivityRestricted},
		{`{"content":"x","thread_id":"t","sensitivity":"local-only"}`, SensitivityLocalOnly},
		{`{"content":"x","thread_id":"t","sensitivity":"restricted"}`, SensitivityRestricted},
		{`{"content":"x","thread_id":"t","sensitivity":"internal"}`, SensitivityInternal},
	}
	for _, c := range cases {
		fake := &fakeConversations{appendResult: AppendResult{ThreadID: "t", TurnID: "u"}}
		var out SendOutput
		dispatchJSON(t, NewDispatcher(fake), ToolSend, c.in, &out)
		if len(fake.gotAppend) != 1 {
			t.Fatalf("%s: Append calls = %d, want 1", c.in, len(fake.gotAppend))
		}
		if got := fake.gotAppend[0].Sensitivity; got != c.want {
			t.Errorf("%s: AppendRequest.Sensitivity = %q, want %q", c.in, got, c.want)
		}
		if out.Sensitivity != fake.gotAppend[0].Sensitivity {
			t.Errorf("%s: echoed %q but carried %q to the mirror", c.in, out.Sensitivity, fake.gotAppend[0].Sensitivity)
		}
	}
}

// TestSendReportsTheBridgeOutcome proves a refused mirror is visible in the
// tool's own result, and that a thread with no mirror carries no field.
func TestSendReportsTheBridgeOutcome(t *testing.T) {
	refused := &BridgeOutcome{Status: BridgeRefused, Reason: "turn sensitivity is not internal or public"}
	fake := &fakeConversations{appendResult: AppendResult{ThreadID: "bridge-telegram:1", TurnID: "u", Bridge: refused}}
	raw, err := NewDispatcher(fake).Dispatch(t.Context(), ToolSend, []byte(`{"content":"x","thread_id":"bridge-telegram:1"}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !strings.Contains(string(raw), `"bridge":{"status":"refused","reason":"turn sensitivity is not internal or public"}`) {
		t.Fatalf("cascade_cpa_send result = %s, want the typed refused bridge outcome", raw)
	}

	plain := &fakeConversations{appendResult: AppendResult{ThreadID: "t", TurnID: "u"}}
	raw, err = NewDispatcher(plain).Dispatch(t.Context(), ToolSend, []byte(`{"content":"x","thread_id":"t"}`))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if strings.Contains(string(raw), `"bridge"`) {
		t.Fatalf("a non-bridge thread's result = %s, want no bridge field", raw)
	}
}
