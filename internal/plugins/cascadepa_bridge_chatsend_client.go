package plugins

import (
	"context"
	"log/slog"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
	"github.com/acamarata/cascade/plugins/cascade-pa/tools"
)

// Purpose (this file): the CLIENT half of fix18's cascade_cpa_send -> Telegram
//   route: the tools.Conversations decorator every process registers
//   (cascadePATools, installed by cascadepa_tools_wiring.go's init) and the
//   bridge.send client it calls. `cascade mcp serve --stdio` is a separate
//   process from the daemon, so the decorator holds no bridge state at all:
//   it dials bridge.send over the daemon socket, exactly as the inner adapter
//   dials chat.append_turn (cascadepa_bridge_chatsend_wiring.go decides).
//
// Inputs: the inner tools.Conversations and each Append's request.
//
// Outputs: the inner Append's result, plus — for a bridge thread only (the
//   daemon's chat-to-thread map decides) — a typed tools.BridgeOutcome in
//   AppendResult.Bridge that cascade_cpa_send returns to its caller (L3):
//   mirrored, refused (with the daemon's fixed reason), skipped or failed.
//
// Constraints:
//   - BRIDGE THREADS ONLY (L2): this process cannot read the bridge's map, so
//     it dials bridge.send for any id telegram.MayBeBridgeThread allows and
//     never for one the map can never hold; a thread the daemon's map does
//     not hold carries no outcome. A caller bridge.send refuses (it is
//     owner-only) also gets no outcome on an id that is not a legacy bridge
//     id; during the daemon's shutdown window every id gets a failed outcome.
//   - BOUNDED: one call, chatSendTimeout past the Append, WithoutCancel so a
//     caller that hangs up does not strand the mirror halfway.
//   - NEVER FAILS THE APPEND: the turn is already recorded; a mirror failure
//     is logged and reported in the outcome, not returned as an error.
//
// SPORT: internal/plugins:cascadepa-bridge-chatsend-client (ADD) — fix18
//   round 3 (split from cascadepa_bridge_chatsend_wiring.go; L2/L3).

// chatSendTimeout bounds a bridge.send round trip past the Append it rides
// on, matching bridgeDispatchTimeout's role for the approval leg.
const chatSendTimeout = 10 * time.Second

// cascadePATools is the ONE tools.Conversations init() registers with
// cascade-pa: the mirroring decorator over the daemon-socket adapter. A
// package variable so a same-package test can reach the exact registered
// instance's rpcDoer seams and prove what init() installed.
var cascadePATools = newBridgeMirroringConversations(newCascadePAConversations(
	client.UnixDialer, cascadePAClientTimeout, runtime.NewDefaultPathProvider))

// bridgeSendClient is bridge.send's client half: the SAME
// dial/timeout/resolvePaths shape cascadePAConversations uses for
// chat.append_turn (cascadepa_wiring.go's rpcDoer seam).
type bridgeSendClient struct {
	dial         client.DialFunc
	timeout      time.Duration
	resolvePaths pathResolver
	// doer, when non-nil, replaces the real client construction — see
	// rpcDoer's own doc comment (cascadepa_wiring.go). Always nil in
	// production; only a same-package test ever sets it.
	doer rpcDoer
}

func newBridgeSendClient() bridgeSendClient {
	return bridgeSendClient{dial: client.UnixDialer, timeout: cascadePAClientTimeout, resolvePaths: runtime.NewDefaultPathProvider}
}

func (c bridgeSendClient) rpc() (rpcDoer, error) {
	if c.doer != nil {
		return c.doer, nil
	}
	paths, err := c.resolvePaths()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "cascade-pa bridge: resolve daemon socket path")
	}
	return client.New(paths.SocketPath(), c.dial, c.timeout), nil
}

// send calls bridge.send over the daemon socket and reports what happened.
// A resolve or RPC failure is logged and becomes a failed (or, for a typed
// policy refusal, refused) outcome — never an error (this file's header).
func (c bridgeSendClient) send(ctx context.Context, threadID, content, sensitivity string) *tools.BridgeOutcome {
	rpc, err := c.rpc()
	if err != nil {
		slog.Default().Warn("cascade-pa bridge: resolve bridge.send client", "error", err)
		return bridgeOutcomeForError(err)
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), chatSendTimeout)
	defer cancel()
	var res BridgeSendResult
	if err := rpc.Do(sctx, MethodBridgeSend, BridgeSendParams{
		ThreadID: threadID, Content: content, Sensitivity: sensitivity,
	}, &res); err != nil {
		if !telegram.IsLegacyBridgeThreadID(threadID) && cascade.HasKind(err, cascade.KindPermissionDenied) {
			return nil // bridge.send is owner-only: a refused caller cannot mirror, so an ordinary thread carries no outcome
		}
		slog.Default().Warn("cascade-pa bridge: bridge.send", "thread_id", threadID, "error", err)
		return bridgeOutcomeForError(err)
	}
	if res.Refused {
		slog.Default().Info("cascade-pa bridge: bridge.send refused", "thread_id", threadID, "reason", res.Reason)
	}
	return bridgeOutcomeFor(res)
}

// bridgeOutcomeFor maps bridge.send's wire result onto the tool's outcome.
func bridgeOutcomeFor(res BridgeSendResult) *tools.BridgeOutcome {
	switch {
	case !res.Mirrored && !res.Refused && res.Reason == reasonNotBridgeThread:
		return nil // the daemon's map does not hold the thread: no outcome
	case res.Mirrored:
		return &tools.BridgeOutcome{Status: tools.BridgeMirrored}
	case res.Refused:
		return &tools.BridgeOutcome{Status: tools.BridgeRefused, Reason: res.Reason}
	default:
		return &tools.BridgeOutcome{Status: tools.BridgeSkipped, Reason: res.Reason}
	}
}

// bridgeOutcomeForError maps a bridge.send error: a typed policy refusal
// from ChatBridge.Send (tier, binding, secret gate) is refused; anything
// else (unreachable daemon, stopped bridge, transport) is failed.
func bridgeOutcomeForError(err error) *tools.BridgeOutcome {
	status := tools.BridgeFailed
	if cascade.HasKind(err, cascade.KindPolicyDenied) {
		status = tools.BridgeRefused
	}
	return &tools.BridgeOutcome{Status: status, Reason: err.Error()}
}

// bridgeMirroringConversations decorates a tools.Conversations: a
// successful Append on a bridge thread also calls bridge.send with the SAME
// content and resolved sensitivity. Thread/Threads/Search delegate unchanged.
type bridgeMirroringConversations struct {
	inner tools.Conversations
	send  bridgeSendClient
}

// newBridgeMirroringConversations wraps inner, which must be non-nil.
func newBridgeMirroringConversations(inner tools.Conversations) *bridgeMirroringConversations {
	return &bridgeMirroringConversations{inner: inner, send: newBridgeSendClient()}
}

var _ tools.Conversations = (*bridgeMirroringConversations)(nil)

// Append records the turn through inner, then — for a bridge thread only —
// calls bridge.send and attaches its outcome (this file's header).
func (c *bridgeMirroringConversations) Append(ctx context.Context, req tools.AppendRequest) (tools.AppendResult, error) {
	res, err := c.inner.Append(ctx, req)
	if err != nil {
		return res, err
	}
	if telegram.MayBeBridgeThread(res.ThreadID) { // only the daemon's map knows; ask it
		res.Bridge = c.send.send(ctx, res.ThreadID, req.Content, req.Sensitivity)
	}
	return res, nil
}

// Thread implements tools.Conversations by delegating unchanged.
func (c *bridgeMirroringConversations) Thread(ctx context.Context, threadID string) (tools.ThreadDetail, error) {
	return c.inner.Thread(ctx, threadID)
}

// Threads implements tools.Conversations by delegating unchanged.
func (c *bridgeMirroringConversations) Threads(ctx context.Context) ([]tools.ThreadSummary, error) {
	return c.inner.Threads(ctx)
}

// Search implements tools.Conversations by delegating unchanged.
func (c *bridgeMirroringConversations) Search(ctx context.Context, req tools.SearchRequest) ([]tools.SearchResult, error) {
	return c.inner.Search(ctx, req)
}
