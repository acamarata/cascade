package plugins

// Purpose (this file): proves fix18 round 2's daemon-side route (K1/K2/K6):
//   BridgeSend's own decisions — sensitivity gate, silent no-op for a
//   non-bridge thread or an unwired/stopped bridge, transport errors
//   propagated unchanged. bridgeMirroringConversations' client-side half
//   lives in the sibling cascadepa_bridge_chatsend_client_test.go (split
//   for Art.10.3's 300-line cap).
//
// SPORT: internal/plugins:cascadepa-bridge-chatsend-wiring/TEST — fix18 round 2.

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	"github.com/acamarata/cascade/plugins/cascade-pa/tools"

	"github.com/acamarata/cascade/pkg/cascade"
)

// recordingChatSender is a minimal cascadepa.ChatBridge fake that records
// every Send call, standing in for a real Telegram transport.
type recordingChatSender struct {
	sent []recordedChatSend
	err  error
}

type recordedChatSend struct {
	threadID string
	body     string
}

func (f *recordingChatSender) Start(context.Context) error { return nil }
func (f *recordingChatSender) Stop(context.Context) error  { return nil }
func (f *recordingChatSender) Drain(context.Context) error { return nil }
func (f *recordingChatSender) Paired() bool                { return true }
func (f *recordingChatSender) Send(_ context.Context, threadID string, body []byte) error {
	f.sent = append(f.sent, recordedChatSend{threadID: threadID, body: string(body)})
	return f.err
}
func (f *recordingChatSender) Receive(context.Context) (<-chan cascadepa.InboundMessage, error) {
	return nil, nil
}
func (f *recordingChatSender) RefusalReport() []cascadepa.RefusalRecord { return nil }

var _ cascadepa.ChatBridge = (*recordingChatSender)(nil)

// resetChatSender snapshots and restores the package-level injected chat
// bridge state, so tests never leak into each other.
func resetChatSender(t *testing.T) {
	t.Helper()
	bridgeChatSenderMu.Lock()
	prevSender, prevEvents, prevClock, prevStopped := bridgeChatSender, bridgeChatEvents, bridgeChatClock, bridgeChatStopped
	bridgeChatSender, bridgeChatEvents, bridgeChatClock, bridgeChatStopped = nil, nil, nil, false
	bridgeChatSenderMu.Unlock()
	t.Cleanup(func() {
		bridgeChatSenderMu.Lock()
		bridgeChatSender, bridgeChatEvents, bridgeChatClock, bridgeChatStopped = prevSender, prevEvents, prevClock, prevStopped
		bridgeChatSenderMu.Unlock()
	})
}

const bridgedThreadID = "bridge-telegram:918273"

// --- BridgeSend (daemon-side) ----------------------------------------------

// TestBridgeSend_BridgedThread_AllowedSensitivity_DeliversOneSend proves
// the happy path: internal/public sensitivity on a bridged thread with a
// wired sender reaches exactly one Send call, and Mirrored is reported.
func TestBridgeSend_BridgedThread_AllowedSensitivity_DeliversOneSend(t *testing.T) {
	resetChatSender(t)
	sender := &recordingChatSender{}
	WireChatBridge(&BridgeRuntime{ChatBridge: sender, Events: newRecordingBus()}, runtime.NewSystemClock())

	res, err := BridgeSend(context.Background(), BridgeSendParams{
		ThreadID: bridgedThreadID, Content: "hello owner", Sensitivity: tools.SensitivityPublic,
	})
	if err != nil {
		t.Fatalf("BridgeSend: %v", err)
	}
	if !res.Mirrored {
		t.Fatalf("result = %+v, want Mirrored true", res)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sender.sent = %+v, want exactly one Send", sender.sent)
	}
	if got := sender.sent[0]; got.threadID != bridgedThreadID || got.body != "hello owner" {
		t.Fatalf("sent = %+v, want thread %s / %q", got, bridgedThreadID, "hello owner")
	}
}

// TestBridgeSend_NonBridgedThread_SilentNoOp proves a thread
// telegram.IsBridgeThread does not recognise is left alone: no error, no
// Send call.
func TestBridgeSend_NonBridgedThread_SilentNoOp(t *testing.T) {
	resetChatSender(t)
	sender := &recordingChatSender{}
	WireChatBridge(&BridgeRuntime{ChatBridge: sender, Events: newRecordingBus()}, runtime.NewSystemClock())

	res, err := BridgeSend(context.Background(), BridgeSendParams{
		ThreadID: "not-a-bridge-thread", Content: "hi", Sensitivity: tools.SensitivityPublic,
	})
	if err != nil {
		t.Fatalf("BridgeSend: %v", err)
	}
	if res.Mirrored || res.Refused || res.Reason != reasonNotBridgeThread {
		t.Fatalf("result = %+v, want skipped with reason %q", res, reasonNotBridgeThread)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sender.sent = %+v, want zero Send calls for a non-bridge thread", sender.sent)
	}
}

// TestBridgeSend_NoSenderWired_SilentNoOp proves the default (no
// WireChatBridge call yet) leaves cascade-pa sends unaffected.
func TestBridgeSend_NoSenderWired_SilentNoOp(t *testing.T) {
	resetChatSender(t)
	res, err := BridgeSend(context.Background(), BridgeSendParams{
		ThreadID: bridgedThreadID, Content: "hi", Sensitivity: tools.SensitivityPublic,
	})
	if err != nil {
		t.Fatalf("BridgeSend: %v", err)
	}
	if res.Mirrored || res.Refused || res.Reason != reasonNoChatBridge {
		t.Fatalf("result = %+v, want skipped with reason %q", res, reasonNoChatBridge)
	}
}

// TestBridgeSend_SensitivityNotAllowed_RefusesAndEmitsEvent proves K2: an
// unset/local-only/restricted turn sensitivity refuses and publishes
// bridge.refused, and the sender is NEVER called — even though the thread
// itself is a real bridge thread with a wired sender. Mutation: ignore
// req.Sensitivity (always treat it as allowed) and this goes RED (the
// sender gets a Send call for a restricted-sensitivity turn).
func TestBridgeSend_SensitivityNotAllowed_RefusesAndEmitsEvent(t *testing.T) {
	// caller sensitivity -> the tier name the content-free event must carry
	cases := map[string]string{"": "restricted", "local-only": "local-only", "restricted": "restricted", "garbage-value": "restricted"}
	for sensitivity, wantTier := range cases {
		t.Run(sensitivity, func(t *testing.T) {
			resetChatSender(t)
			sender := &recordingChatSender{}
			bus := newRecordingBus()
			WireChatBridge(&BridgeRuntime{ChatBridge: sender, Events: bus}, runtime.NewSystemClock())

			res, err := BridgeSend(context.Background(), BridgeSendParams{
				ThreadID: bridgedThreadID, Content: "turn-body-marker", Sensitivity: sensitivity,
			})
			if err != nil {
				t.Fatalf("BridgeSend: %v", err)
			}
			if res.Mirrored || !res.Refused || res.Reason != reasonTurnSensitivityRefused {
				t.Fatalf("result = %+v for sensitivity %q, want Refused with reason %q",
					res, sensitivity, reasonTurnSensitivityRefused)
			}
			if len(sender.sent) != 0 {
				t.Fatalf("sender.sent = %+v, want zero Send calls for sensitivity %q", sender.sent, sensitivity)
			}
			got := bus.events()
			if len(got) != 1 || got[0].namespace != "bridge" || string(got[0].kind) != "bridge.refused" {
				t.Fatalf("bus.events() = %+v, want exactly one bridge.refused", got)
			}
			if strings.Contains(got[0].payload, "turn-body-marker") || !strings.Contains(got[0].payload, wantTier) {
				t.Fatalf("bridge.refused payload %q must be content-free and carry the tier name %q", got[0].payload, wantTier)
			}
		})
	}
}

// TestBridgeSend_AfterStop_RefusesTypedUnavailable proves K6: once
// UnwireChatBridge has run (the composition root's Stop wrapper), a
// bridge.send call refuses typed-unavailable rather than reaching a
// stopped module or silently no-opping.
func TestBridgeSend_AfterStop_RefusesTypedUnavailable(t *testing.T) {
	resetChatSender(t)
	sender := &recordingChatSender{}
	WireChatBridge(&BridgeRuntime{ChatBridge: sender, Events: newRecordingBus()}, runtime.NewSystemClock())
	if err := UnwireChatBridge(context.Background()); err != nil {
		t.Fatalf("UnwireChatBridge: %v", err)
	}

	_, err := BridgeSend(context.Background(), BridgeSendParams{
		ThreadID: bridgedThreadID, Content: "hi", Sensitivity: tools.SensitivityPublic,
	})
	if err != ErrBridgeSendUnavailable {
		t.Fatalf("BridgeSend error = %v, want ErrBridgeSendUnavailable", err)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sender.sent = %+v, want zero Send calls after Stop", sender.sent)
	}
}

// TestBridgeSend_SenderError_Propagated proves a real Send failure
// (privacy/binding/secret refusal inside TelegramBridge.Send itself) is
// returned to the RPC caller unchanged, not swallowed.
func TestBridgeSend_SenderError_Propagated(t *testing.T) {
	resetChatSender(t)
	wantErr := cascade.New(cascade.KindPolicyDenied, "test: send refused")
	sender := &recordingChatSender{err: wantErr}
	WireChatBridge(&BridgeRuntime{ChatBridge: sender, Events: newRecordingBus()}, runtime.NewSystemClock())

	_, err := BridgeSend(context.Background(), BridgeSendParams{
		ThreadID: bridgedThreadID, Content: "hi", Sensitivity: tools.SensitivityInternal,
	})
	if err != wantErr {
		t.Fatalf("BridgeSend error = %v, want the sender's own error unchanged", err)
	}
}

// TestWireChatBridge_NilOrDisabled_InjectsNothing proves WireChatBridge's
// own no-op paths.
func TestWireChatBridge_NilOrDisabled_InjectsNothing(t *testing.T) {
	resetChatSender(t)
	clock := runtime.NewSystemClock()
	for _, rt := range []*BridgeRuntime{nil, {ChatBridge: nil}} {
		WireChatBridge(rt, clock)
		res, err := BridgeSend(context.Background(), BridgeSendParams{
			ThreadID: bridgedThreadID, Content: "hi", Sensitivity: tools.SensitivityPublic,
		})
		if err != nil || res.Reason != reasonNoChatBridge {
			t.Fatalf("WireChatBridge(%+v): BridgeSend = %+v, %v, want skipped (nothing injected)", rt, res, err)
		}
	}
}
