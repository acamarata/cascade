// Purpose (this file): the composition-root adapter for the §5.24 PRODUCER
//   leg (P1-E23-W5-S48-T4, T0 D4) — the ONE place allowed to import both
//   internal/policy (BridgeSender, BridgeLeg, BridgeRef) and
//   plugins/cascade-pa/telegram (TelegramApprovalSender), because
//   plugins/cascade-pa itself may not import internal/ (Art.10.2).
//
// Inputs: the same collaborators cascadepa_bridge_wiring.go's enabledBridge
//   already assembles (subject, the durable BridgeState, the W/S-48.T1
//   nonce store, the module's BotClient, the clock) — this file adds no new
//   ones, it only wraps what enabledBridge already builds into the shape
//   ApprovalQueueConfig.Bridge (approval_queue.go) takes.
//
// Outputs: newTelegramBridgeSender (policy.BridgeSender over
//   TelegramApprovalSender), newApprovalBridgeLeg (the one-call composition
//   helper: build the sender, wrap it, hand back a ready *policy.BridgeLeg).
//
// UNEXPORTED ON PURPOSE, mirroring this SAME file's own precedent
// (newTelegramApprovalService, telegram_approval_wiring.go): every adapter
// internal/plugins builds for the bridge is package-private. The caller
// stays inside this same package (enabledBridge, cascadepa_bridge_wiring.go)
// even after the FLAG-0 fix below, so exporting either symbol was never
// required — see GAP CLOSED for why the earlier draft of this file
// (correctly) predicted a cmd/cascade caller that turned out not to be the
// right shape.
//
// SIBLING FILE, NOT AN EDIT (LANE-RULES §19): cascadepa_bridge_wiring.go —
//   the natural composition root, where enabledBridge already builds
//   module/stores/subject — carries another ticket's own concurrent work
//   (the S-48.T2 chat hook), so this file stays a sibling with its own
//   entry points rather than growing that file past what T0 authorized
//   there (add the ApprovalBridge field, and fill it — nothing else; that
//   file is also at Art.10.3's 300-line cap).
//
// GAP CLOSED (P1-E23-W5-S48-T4 FIX-0, T0 ruling). The CR's FLAG-0 found the
//   original "one remaining line" plan unreachable: enabledBridge's
//   subject/state/stores.Callback/module.Client()/clock exist only INSIDE
//   enabledBridge (cascadepa_bridge_wiring.go), built from cmd/cascade's
//   wireCascadePABridge (plugin_rpc.go) — a call path that starts and ends
//   AFTER wirePolicy already built and returned the queue
//   (cmd/cascade/daemon_unix_policy.go). A queue cannot take a bridge it
//   does not have yet at construction time, so the fix runs the other
//   direction: SetBridge (internal/policy/approval_queue.go) arms an
//   ALREADY-BUILT queue later, and the queue itself reaches this package
//   via a one-time injection (SetBridgeApprovalQueue, called from
//   cmd/cascade/daemon_unix.go beside wireCascadePAInstallHostDeps' own
//   identical D1 precedent) rather than being threaded through
//   buildRPCServer's option plumbing or plugin_rpc.go's signatures — the
//   same "composition root is always in scope, smallest hunk in a shared
//   file" reasoning daemon_unix_cascadepa_install.go's own header states
//   for the same class of gap. WireApprovalBridge below is the single call
//   cmd/cascade/plugin_rpc.go's wireCascadePABridge makes once rt is real.
//
// SPORT: internal/plugins:cascadepa-bridge-send-wiring (ADD/CHG) —
//   P1-E23-W5-S48-T4 producer leg, T0 D4, FIX-0.

package plugins

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/policy"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

// telegramBridgeSender adapts *telegram.TelegramApprovalSender to
// policy.BridgeSender: BridgeRef carries only a request id (approval_token.go),
// so there is nothing to translate but ref.RequestID.String().
type telegramBridgeSender struct {
	sender *telegram.TelegramApprovalSender
}

// newTelegramBridgeSender builds a policy.BridgeSender over sender.
func newTelegramBridgeSender(sender *telegram.TelegramApprovalSender) policy.BridgeSender {
	return &telegramBridgeSender{sender: sender}
}

// Send implements policy.BridgeSender.
func (a *telegramBridgeSender) Send(ctx context.Context, ref policy.BridgeRef) error {
	return a.sender.Send(ctx, ref.RequestID.String())
}

// compile-time proof the adapter really satisfies the seam.
var _ policy.BridgeSender = (*telegramBridgeSender)(nil)

// newApprovalBridgeLeg is the one-call composition helper: build a
// TelegramApprovalSender over the given collaborators, wrap it in the
// policy.BridgeSender adapter, and hand back a ready *policy.BridgeLeg for
// ApprovalQueueConfig.Bridge. Every argument is one enabledBridge
// (cascadepa_bridge_wiring.go) already has in hand by the time it builds
// the module, so wiring this in is genuinely the one line this file's
// header names — once cmd/cascade's queue construction can reach it.
func newApprovalBridgeLeg(subject string, state cascadepa.BridgeState,
	callbacks *cascadepa.CallbackNonceStore, client *telegram.BotClient,
	clock cascadepa.PairClock) (*policy.BridgeLeg, error) {
	sender := telegram.NewTelegramApprovalSender(telegram.TelegramApprovalSenderDeps{
		Subject: subject, State: state, Callbacks: callbacks, Client: client, Clock: clock,
	})
	return policy.NewBridgeLeg(newTelegramBridgeSender(sender))
}

// bridgeApprovalQueueMu guards bridgeApprovalQueue: the daemon's ONE
// running approval queue, injected here before any bridge is assembled
// (SetBridgeApprovalQueue) so WireApprovalBridge can arm it once
// enabledBridge (cascadepa_bridge_wiring.go) has actually built a
// producer leg. See this file's GAP CLOSED header for why the injection
// runs this direction rather than the queue taking the leg at
// construction time.
var (
	bridgeApprovalQueueMu sync.Mutex
	bridgeApprovalQueue   policy.ApprovalQueue
)

// bridgeArmableQueue is the narrow slice of *policy.StoreApprovals this
// file needs. SetBridge is deliberately NOT part of policy.ApprovalQueue
// (approval_queue.go): most callers never wire a bridge at all, so this
// file asserts for the one method it needs rather than widening the
// shared interface for a single caller.
type bridgeArmableQueue interface {
	SetBridge(policy.BridgeNotifier) error
}

// SetBridgeApprovalQueue injects the daemon's one running approval queue,
// once, before any bridge is assembled. The ONLY caller is the
// composition root (cmd/cascade/daemon_unix.go, beside
// wireCascadePAInstallHostDeps' identical D1 call). A nil queue clears
// the injection: WireApprovalBridge then arms nothing, which is the same
// fail-safe "the bridge still runs, it just never notifies anything"
// reading ApprovalQueueConfig.Bridge's own doc comment gives an unset
// value.
func SetBridgeApprovalQueue(q policy.ApprovalQueue) {
	bridgeApprovalQueueMu.Lock()
	defer bridgeApprovalQueueMu.Unlock()
	bridgeApprovalQueue = q
}

// WireApprovalBridge arms the injected queue with rt's own producer leg
// (P1-E23-W5-S48-T4 FIX-0). It is deliberately best-effort and returns
// nothing: rt nil, a disabled bridge (rt.ApprovalBridge == nil), no
// injected queue, or a queue whose concrete type does not satisfy
// bridgeArmableQueue all leave nothing armed rather than failing the
// daemon's startup — wireCascadePABridge's own header states that exact
// posture for the bridge as a whole. A real refusal (a second SetBridge
// call) is logged, since there is nothing a caller with no return value
// could usefully do about it.
//
// FLAG-B (S-48.T4 producer confirming review): the two SILENT-RETURN paths
// below (no queue injected yet, or the injected queue's concrete type does
// not implement bridgeArmableQueue) used to leave an operator with no way
// to tell a genuine mis-order — daemon_unix.go's SetBridgeApprovalQueue
// call landing AFTER buildRPCServer, or wireCascadePABridge running before
// wirePolicy at all — apart from "notifications never arrive". Both now log
// a Warn through the same slog.Default() seam the SetBridge-failure branch
// already uses, so the composition root's own ordering contract
// (TestDaemonUnixBridgeQueueInjectionPrecedesRPCServer, cmd/cascade) has an
// observable counterpart at runtime if it is ever violated outside a test.
//
// The leg is wrapped in asyncBridgeNotifier so notification never runs on
// the enqueuer's own goroutine or context (the CR's disclosed P7 gap): a
// slow or hung Telegram call must not delay the RPC caller that just
// admitted an ask-tier action.
func WireApprovalBridge(rt *BridgeRuntime) {
	if rt == nil || rt.ApprovalBridge == nil {
		return
	}
	bridgeApprovalQueueMu.Lock()
	queue := bridgeApprovalQueue
	bridgeApprovalQueueMu.Unlock()
	if queue == nil {
		slog.Default().Warn("cascade-pa bridge: no approval queue was injected before WireApprovalBridge ran; "+
			"the producer leg is armed on nothing", "subject", rt.Subject)
		return
	}
	armable, ok := queue.(bridgeArmableQueue)
	if !ok {
		slog.Default().Warn("cascade-pa bridge: the injected approval queue does not implement SetBridge; "+
			"the producer leg is armed on nothing", "subject", rt.Subject, "queue_type", fmt.Sprintf("%T", queue))
		return
	}
	if err := armable.SetBridge(asyncBridgeNotifier{inner: rt.ApprovalBridge}); err != nil {
		slog.Default().Warn("cascade-pa bridge: arming approval queue producer leg", "error", err)
	}
}

// bridgeDispatchTimeout bounds the detached goroutine asyncBridgeNotifier
// spawns, so a stalled Telegram call cannot leak forever.
const bridgeDispatchTimeout = 30 * time.Second

// asyncBridgeNotifier decorates a policy.BridgeNotifier so Dispatch never
// blocks its caller. notifyBridge (internal/policy/bridge_leg.go) calls
// Dispatch synchronously, on the RPC caller's own ctx, from inside
// Enqueue — a hung Telegram call would otherwise delay the very Enqueue
// call that just admitted the action (CR P7).
type asyncBridgeNotifier struct {
	inner policy.BridgeNotifier
}

// Dispatch implements policy.BridgeNotifier by handing the real work to a
// detached goroutine, bounded by its own timeout rather than the caller's
// ctx (which ends the instant Enqueue returns). The result is
// unobservable by design — notifyBridge already discards it (its own doc
// comment: "a transport failure must never fail admission") — so the only
// contract this type owes is "never block the caller", kept by always
// returning nil immediately.
func (a asyncBridgeNotifier) Dispatch(_ context.Context, verb string, entry policy.PendingEntry) error {
	go func() {
		dctx, cancel := context.WithTimeout(context.Background(), bridgeDispatchTimeout)
		defer cancel()
		_ = a.inner.Dispatch(dctx, verb, entry)
	}()
	return nil
}

// compile-time proof the decorator really satisfies the seam it wraps.
var _ policy.BridgeNotifier = asyncBridgeNotifier{}
