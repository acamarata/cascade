package plugins

// Purpose (this file): fix18 round 3's client-side proofs — (L2) the
//   decorator dials bridge.send for bridge threads ONLY; (L3) every
//   bridge.send answer becomes a typed tools.BridgeOutcome the tool caller
//   sees; (decorator) init() really registered the mirroring decorator, proven
//   through the builtin cascade-pa plugin's own DispatchTool.
//
// SPORT: internal/plugins:cascadepa-bridge-chatsend-client/TEST — fix18 round 3.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/plugins/cascade-pa/tools"
)

// TestBridgeMirroringConversations_NonBridgeThread_NeverCallsRPC proves the
// L2 pre-filter: an ordinary thread's Append never dials bridge.send and
// carries no outcome. Dropping the IsBridgeThread filter turns this RED.
func TestBridgeMirroringConversations_NonBridgeThread_NeverCallsRPC(t *testing.T) {
	for _, threadID := range []string{"thread-7", "bridge-telegram:+918273", "bridge-telegram:0918273"} {
		doer := &recordingBridgeSendDoer{}
		inner := &stubConversations{res: tools.AppendResult{ThreadID: threadID, TurnID: "turn-1"}}
		conv := &bridgeMirroringConversations{inner: inner, send: bridgeSendClient{doer: doer}}
		res, err := conv.Append(context.Background(), tools.AppendRequest{ThreadID: threadID, Role: "user", Content: "hi"})
		if err != nil {
			t.Fatalf("%s: Append: %v", threadID, err)
		}
		if len(doer.calls) != 0 || res.Bridge != nil {
			t.Fatalf("%s: bridge.send calls = %+v, Bridge = %+v; want none", threadID, doer.calls, res.Bridge)
		}
	}
}

// TestBridgeMirroringConversations_ReportsEveryOutcome pins the L3 mapping
// from bridge.send's answer to the outcome cascade_cpa_send returns.
func TestBridgeMirroringConversations_ReportsEveryOutcome(t *testing.T) {
	cases := []struct {
		name   string
		doer   *recordingBridgeSendDoer
		status string
		reason string
	}{
		{"refused", &recordingBridgeSendDoer{res: &BridgeSendResult{Refused: true, Reason: reasonTurnSensitivityRefused}},
			tools.BridgeRefused, reasonTurnSensitivityRefused},
		{"skipped", &recordingBridgeSendDoer{res: &BridgeSendResult{Reason: reasonNoChatBridge}},
			tools.BridgeSkipped, reasonNoChatBridge},
		{"policy error", &recordingBridgeSendDoer{err: cascade.New(cascade.KindPolicyDenied, "not the paired binding")},
			tools.BridgeRefused, "not the paired binding"},
		{"unavailable", &recordingBridgeSendDoer{err: ErrBridgeSendUnavailable},
			tools.BridgeFailed, "bridge.send is unavailable"},
	}
	for _, c := range cases {
		inner := &stubConversations{res: tools.AppendResult{ThreadID: bridgedThreadID, TurnID: "turn-1"}}
		conv := &bridgeMirroringConversations{inner: inner, send: bridgeSendClient{doer: c.doer}}
		res, err := conv.Append(context.Background(), tools.AppendRequest{ThreadID: bridgedThreadID, Content: "hi"})
		if err != nil {
			t.Fatalf("%s: Append: %v (a mirror outcome must never fail the Append)", c.name, err)
		}
		if res.Bridge == nil || res.Bridge.Status != c.status || !strings.Contains(res.Bridge.Reason, c.reason) {
			t.Fatalf("%s: Bridge = %+v, want status %q with reason containing %q", c.name, res.Bridge, c.status, c.reason)
		}
	}
}

// TestCascadePAToolsInit_RegistersTheMirroringDecorator proves what init()
// installed: cascade_cpa_send, dispatched through the builtin cascade-pa
// plugin exactly as the MCP server reaches it, runs the append AND
// bridge.send on the registered instance's seams, and returns the outcome.
// An init() registering the bare adapter records the turn but never calls
// bridge.send and returns no outcome, turning this RED.
func TestCascadePAToolsInit_RegistersTheMirroringDecorator(t *testing.T) {
	home, err := os.MkdirTemp("", "cpa")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("CASCADE_HOME", home) // a mis-registered adapter must never reach a real daemon

	inner, ok := cascadePATools.inner.(*cascadePAConversations)
	if !ok {
		t.Fatalf("cascadePATools.inner = %T, want *cascadePAConversations", cascadePATools.inner)
	}
	appendDoer := &methodRPCDoer{appendRes: appendTurnResult{ThreadID: bridgedThreadID, TurnID: "u1"},
		threadErr: cascade.New(cascade.KindUnavailable, "test: no read-back")}
	sendDoer := &recordingBridgeSendDoer{}
	prevInner, prevSend := inner.doer, cascadePATools.send.doer
	inner.doer, cascadePATools.send.doer = appendDoer, sendDoer
	t.Cleanup(func() { inner.doer, cascadePATools.send.doer = prevInner, prevSend })

	var out []byte
	for _, reg := range plugin.Builtins() {
		if reg.Manifest.ID == "cascade-pa" {
			out, err = reg.Handlers.DispatchTool(context.Background(), tools.ToolSend,
				[]byte(`{"content":"hi","thread_id":"`+bridgedThreadID+`","sensitivity":"internal"}`))
		}
	}
	if err != nil {
		t.Fatalf("DispatchTool(cascade_cpa_send): %v", err)
	}
	if len(sendDoer.calls) != 1 || sendDoer.calls[0].Sensitivity != tools.SensitivityInternal {
		t.Fatalf("bridge.send calls = %+v, want one carrying the turn's sensitivity", sendDoer.calls)
	}
	if !strings.Contains(string(out), `"bridge":{"status":"mirrored"}`) {
		t.Fatalf("cascade_cpa_send result = %s, want the mirrored outcome", out)
	}
}
