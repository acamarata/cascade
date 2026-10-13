package plugins

// Purpose (this file): fix18 round 3's lifecycle proofs for bridge.send —
//   (L1) UnwireChatBridge refuses new calls at once and then WAITS for every
//   admitted one, so the composition root can drain before the module stops;
//   (keepbridge) the enabled runtime really carries the chat bridge the real
//   constructor built, and WireChatBridge routes bridge.send into its Send.
//
// SPORT: internal/plugins:cascadepa-bridge-chatsend-wiring/TEST — fix18 round 3.

import (
	"context"
	"encoding/json"
	"errors"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	"github.com/acamarata/cascade/plugins/cascade-pa/tools"
)

// blockingChatSender holds Send until release is closed, reporting arrival.
type blockingChatSender struct {
	recordingChatSender
	arrived chan struct{}
	release chan struct{}
}

func (b *blockingChatSender) Send(ctx context.Context, threadID string, body []byte) error {
	close(b.arrived)
	<-b.release
	return b.recordingChatSender.Send(ctx, threadID, body)
}

var _ cascadepa.ChatBridge = (*blockingChatSender)(nil)

// TestUnwireChatBridge_RefusesNewAndWaitsForAdmitted is L1's plugin-side
// proof. While one bridge.send is held inside Send: Unwire with a short
// budget reports the budget ran out (it is waiting), a NEW bridge.send is
// refused typed-unavailable without reaching Send, and once the held call
// returns, Unwire completes. An Unwire that only flipped the flag returns
// nil at once and turns the first assertion RED.
func TestUnwireChatBridge_RefusesNewAndWaitsForAdmitted(t *testing.T) {
	resetChatSender(t)
	sender := &blockingChatSender{arrived: make(chan struct{}), release: make(chan struct{})}
	WireChatBridge(&BridgeRuntime{ChatBridge: sender, Events: newRecordingBus()}, runtime.NewSystemClock())
	params := BridgeSendParams{ThreadID: bridgedThreadID, Content: "held", Sensitivity: tools.SensitivityInternal}

	held := make(chan error, 1)
	go func() {
		_, err := BridgeSend(context.Background(), params)
		held <- err
	}()
	<-sender.arrived

	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := UnwireChatBridge(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("UnwireChatBridge with a call in flight = %v, want context.DeadlineExceeded", err)
	}
	if _, err := BridgeSend(context.Background(), params); err != ErrBridgeSendUnavailable {
		t.Fatalf("bridge.send during shutdown = %v, want ErrBridgeSendUnavailable", err)
	}

	close(sender.release)
	if err := <-held; err != nil {
		t.Fatalf("the admitted bridge.send: %v", err)
	}
	if err := UnwireChatBridge(context.Background()); err != nil {
		t.Fatalf("UnwireChatBridge after the admitted call returned: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("Send calls = %d, want exactly the one admitted before shutdown", len(sender.sent))
	}
}

// TestNewCascadePABridge_ChatBridgeReachesBridgeSend proves the enabled
// runtime carries the chat bridge the real constructor built (not nil, not
// discarded) and that WireChatBridge routes bridge.send into THAT bridge's
// real Send: an unbound bridge thread is refused by Send's own binding gate
// (an error, plus one bridge.refused on the runtime's bus). A runtime whose
// ChatBridge is nil answers "skipped" with no error and turns this RED.
func TestNewCascadePABridge_ChatBridgeReachesBridgeSend(t *testing.T) {
	resetChatSender(t)
	ctx := context.Background()
	deps, bus, dataDir := enabledBridgeDeps(t)
	seedBridgeThread(t, dataDir, 555000111, "bridge-telegram:555000111") // mapped, never paired
	rt, err := NewCascadePABridge(ctx, deps)
	if err != nil {
		t.Fatalf("NewCascadePABridge: %v", err)
	}
	closeBridgeRuntime(t, rt)
	if rt.ChatBridge == nil || rt.Events == nil {
		t.Fatalf("enabled runtime ChatBridge=%v Events=%v, want both kept", rt.ChatBridge, rt.Events)
	}
	WireChatBridge(rt, runtime.NewSystemClock())

	res, err := BridgeSend(ctx, BridgeSendParams{
		ThreadID: "bridge-telegram:555000111", Content: "never delivered", Sensitivity: tools.SensitivityInternal,
	})
	if err == nil {
		t.Fatalf("bridge.send to an unbound chat = %+v, nil; want the real Send's refusal", res)
	}
	// Windows tier-2 refuses at Send's platform gate first, before any event.
	want := 1
	if goruntime.GOOS == "windows" {
		want = 0
	}
	if n := len(bus.events()); n != want {
		t.Fatalf("bridge events = %d, want %d bridge.refused from the real Send", n, want)
	}
}

// TestRegisterBridgeSend_DispatchesAndStartsFresh drives the registered
// handler through a real *rpc.Registry: a previous composition's stopped
// state does not leak into a new registration (it answers skipped, not
// unavailable), and malformed params are refused as invalid input.
func TestRegisterBridgeSend_DispatchesAndStartsFresh(t *testing.T) {
	resetChatSender(t)
	if err := UnwireChatBridge(context.Background()); err != nil {
		t.Fatalf("UnwireChatBridge: %v", err)
	}
	registry := rpc.NewRegistry()
	RegisterBridgeSend(registry)

	params, err := json.Marshal(BridgeSendParams{ThreadID: bridgedThreadID, Content: "x", Sensitivity: "public"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: MethodBridgeSend, Params: params})
	if errObj != nil {
		t.Fatalf("bridge.send after a fresh registration = %+v, want skipped", errObj)
	}
	if res, ok := raw.(BridgeSendResult); !ok || res.Reason != reasonNoChatBridge {
		t.Fatalf("bridge.send result = %#v, want skipped with reason %q", raw, reasonNoChatBridge)
	}
	_, errObj = registry.Dispatch(context.Background(), &rpc.Request{Method: MethodBridgeSend, Params: json.RawMessage(`[1]`)})
	if errObj == nil || !strings.Contains(errObj.Message, "decode params") {
		t.Fatalf("malformed bridge.send = %+v, want a decode refusal", errObj)
	}
}

// TestBridgeSendClient_Send_PathResolutionFailure_ReportsFailed proves an
// unresolvable daemon socket becomes a visible failed outcome, not a panic
// or a silent success.
func TestBridgeSendClient_Send_PathResolutionFailure_ReportsFailed(t *testing.T) {
	c := bridgeSendClient{resolvePaths: func() (runtime.PathProvider, error) {
		return nil, cascade.New(cascade.KindUnavailable, "test: no home directory")
	}}
	got := c.send(context.Background(), bridgedThreadID, "hi", "internal")
	if got.Status != tools.BridgeFailed || !strings.Contains(got.Reason, "no home directory") {
		t.Fatalf("send = %+v, want failed naming the resolution error", got)
	}
}
