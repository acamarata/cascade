package plugins

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
	"github.com/acamarata/cascade/plugins/cascade-pa/tools"
)

// Purpose (this file): the DAEMON half of fix18's route from cascade_cpa_send
//   to the Telegram bridge's outbound leg (TelegramBridge.Send): the
//   bridge.send RPC method, its decision, and the lifecycle that arms and
//   disarms it. The client half (the tools.Conversations decorator every
//   process registers, including `cascade mcp serve --stdio`) lives in
//   cascadepa_bridge_chatsend_client.go and reaches this file only over the
//   daemon socket, because only the daemon process holds a live bridge.
//
// Inputs: a *BridgeRuntime (WireChatBridge), the daemon's *rpc.Registry
//   (RegisterBridgeSend) and bridge.send's wire params.
//
// Outputs: for a bridge thread with a wired bridge and an internal/public
//   turn sensitivity, one real ChatBridge.Send call, which itself enforces
//   the thread's tier, the paired binding and the secret gate; a narrower
//   turn sensitivity is a content-free bridge.refused event plus a Refused
//   result; no bridge wired or not a bridge thread is a Skipped result with
//   its reason; a stopped bridge is ErrBridgeSendUnavailable.
//
// Constraints:
//   - SHUTDOWN ORDER (fix18 L1). UnwireChatBridge refuses every NEW call and
//     then waits for every ADMITTED one, so the composition root can drain the
//     chat bridge and only then stop the module and close its state: no
//     bridge.send can reach a stopped module or a closed store (which would
//     misreport itself as "not bound" with a false bridge.refused event).
//   - NO SECOND COPY OF A GATE. This file adds only the one check
//     ChatBridge.Send cannot make, the turn's own declared sensitivity.
//
// SPORT: internal/plugins:cascadepa-bridge-chatsend-wiring (CHANGED) —
//   fix18 rounds 2-3 (K1/K2/K6, L1/L3).

// MethodBridgeSend is the daemon's bridge.send RPC method name (K1).
const MethodBridgeSend = "bridge.send"

// BridgeSendParams is bridge.send's wire request: a turn already committed
// via chat.append_turn, offered for the chat-bridge mirror.
type BridgeSendParams struct {
	ThreadID    string `json:"thread_id"`
	Content     string `json:"content"`
	Sensitivity string `json:"sensitivity"`
}

// BridgeSendResult is bridge.send's wire response. Exactly one of Mirrored
// and Refused is true, or neither (skipped, with Reason saying why).
type BridgeSendResult struct {
	Mirrored bool   `json:"mirrored"`
	Refused  bool   `json:"refused,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// ErrBridgeSendUnavailable is bridge.send's typed refusal once the bridge
// has begun stopping (K6/L1): a caller that raced the daemon's shutdown gets
// an honest "unavailable", never a silent no-op.
var ErrBridgeSendUnavailable = cascade.New(cascade.KindUnavailable,
	"cascade-pa bridge: the chat bridge is stopped; bridge.send is unavailable")

// bridge.send's fixed, non-interpolated reasons.
const (
	reasonTurnSensitivityRefused = "turn sensitivity is not internal or public"
	reasonNoChatBridge           = "the daemon runs no enabled chat bridge"
	reasonNotBridgeThread        = "the thread is not a chat-bridge thread"
)

// unwirePollInterval is how often UnwireChatBridge re-checks the admitted
// count; the wait itself is bounded by the caller's ctx.
const unwirePollInterval = 5 * time.Millisecond

// The daemon's one enabled chat bridge, its event bus, its clock and its
// stopped flag, guarded by bridgeChatSenderMu; bridgeSendInFlight counts
// admitted calls (incremented only under the mutex, while not stopped).
var (
	bridgeChatSenderMu sync.Mutex
	bridgeChatSender   cascadepa.ChatBridge
	bridgeChatEvents   BridgeEventPublisher
	bridgeChatClock    runtime.Clock
	bridgeChatStopped  bool
	bridgeSendInFlight atomic.Int64
)

// WireChatBridge injects rt's ChatBridge and Events plus clock, following
// WireApprovalBridge's pattern. A nil rt or a disabled bridge
// (rt.ChatBridge == nil) injects nothing: bridge.send then answers Skipped.
func WireChatBridge(rt *BridgeRuntime, clock runtime.Clock) {
	if rt == nil || rt.ChatBridge == nil {
		return
	}
	bridgeChatSenderMu.Lock()
	bridgeChatSender, bridgeChatEvents, bridgeChatClock = rt.ChatBridge, rt.Events, clock
	bridgeChatStopped = false
	bridgeChatSenderMu.Unlock()
}

// UnwireChatBridge refuses every new bridge.send (typed unavailable), then
// waits until every already-admitted call has returned or ctx ends — the
// FIRST step of the bridge's shutdown (L1, this file's header).
func UnwireChatBridge(ctx context.Context) error {
	bridgeChatSenderMu.Lock()
	bridgeChatSender, bridgeChatStopped = nil, true
	bridgeChatSenderMu.Unlock()
	ticker := time.NewTicker(unwirePollInterval)
	defer ticker.Stop()
	for bridgeSendInFlight.Load() > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return nil
}

// admitBridgeSend returns the wired bridge and counts the call in flight, or
// ErrBridgeSendUnavailable once stopped. A nil sender (nothing wired) is not
// counted; the caller decrements bridgeSendInFlight exactly when sender is
// non-nil.
func admitBridgeSend() (sender cascadepa.ChatBridge, events BridgeEventPublisher, clock runtime.Clock, err error) {
	bridgeChatSenderMu.Lock()
	defer bridgeChatSenderMu.Unlock()
	if bridgeChatStopped {
		return nil, nil, nil, ErrBridgeSendUnavailable
	}
	if bridgeChatSender != nil {
		bridgeSendInFlight.Add(1)
	}
	return bridgeChatSender, bridgeChatEvents, bridgeChatClock, nil
}

// BridgeSend implements bridge.send's decision inside the daemon process
// (this file's header gives the outcome table).
func BridgeSend(ctx context.Context, params BridgeSendParams) (BridgeSendResult, error) {
	sender, events, clock, err := admitBridgeSend()
	if err != nil {
		return BridgeSendResult{}, err
	}
	if sender == nil {
		return BridgeSendResult{Reason: reasonNoChatBridge}, nil
	}
	defer bridgeSendInFlight.Add(-1)
	if !telegram.IsBridgeThread(params.ThreadID) {
		return BridgeSendResult{Reason: reasonNotBridgeThread}, nil
	}
	resolved := tools.ResolveSensitivity(params.Sensitivity)
	if resolved != tools.SensitivityInternal && resolved != tools.SensitivityPublic {
		emitTurnSensitivityRefused(ctx, events, clock, params.ThreadID, resolved)
		return BridgeSendResult{Refused: true, Reason: reasonTurnSensitivityRefused}, nil
	}
	if err := sender.Send(ctx, params.ThreadID, []byte(params.Content)); err != nil {
		return BridgeSendResult{}, err
	}
	return BridgeSendResult{Mirrored: true}, nil
}

// RegisterBridgeSend registers bridge.send on the daemon's registry: decode
// the wire params, run BridgeSend, return its result or error unchanged. It
// is the first step of a daemon's bridge composition, so it also starts the
// process's bridge.send state afresh (unwired, not stopped): a composition
// never inherits a previous one's stopped bridge.
func RegisterBridgeSend(registry *rpc.Registry) {
	bridgeChatSenderMu.Lock()
	bridgeChatSender, bridgeChatEvents, bridgeChatClock, bridgeChatStopped = nil, nil, nil, false
	bridgeChatSenderMu.Unlock()
	registry.Register(MethodBridgeSend, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var params BridgeSendParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, cascade.Wrap(cascade.KindInvalidInput, err, "bridge.send: decode params")
		}
		return BridgeSend(ctx, params)
	})
}

// emitTurnSensitivityRefused publishes bridge.refused for the turn
// sensitivity gate — the one refusal ChatBridge.Send can never see —
// through the SAME journal and wire shape the inbound divergence sink uses
// (bridgeDivergenceJournal, cascadepa_bridge_chat_wiring.go).
func emitTurnSensitivityRefused(ctx context.Context, events BridgeEventPublisher, clock runtime.Clock, threadID, resolved string) {
	bridgeDivergenceJournal{bus: events}.EmitRefused(ctx, telegram.RefusalEvent{
		ThreadID: threadID, ResolvedTier: resolved, Reason: reasonTurnSensitivityRefused,
		CorrelationID: "bridge.send:" + threadID, At: clock.Now().UTC(),
	})
}
