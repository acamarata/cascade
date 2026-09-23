package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

// Purpose (this file): the W/S-48.T2 chat-parity composition root —
//   NewCascadePABridgeChatHandler assembles the three host-mediated seams
//   egress.go declares (telegram.ChatService, ThreadPrivacyResolver,
//   DivergenceSink) and hands them to telegram.NewTelegramBridge, which
//   registers itself as the module's HandlerText.
//
// WHY THIS IS A SIBLING FILE, NOT A HUNK IN cascadepa_bridge_wiring.go.
//   That file's enabledBridge is the natural call site (it already
//   constructs `module` and comments "HandlerText still records rather
//   than drops (W/S-48.T2 replaces this)") but is under concurrent edit by
//   the W/S-48.T4 approval lane at the time this ticket landed (LANE-RULES
//   composition-root precedent: add a NEW sibling file, report the exact
//   hunk for T0 to merge, never skip the wiring). The merge this needs, once
//   T4's edits land, is: replace
//     module.RegisterHandler(telegram.HandlerText, journal.unroutedHandler(telegram.HandlerText))
//   with
//     chatBridge, err := NewCascadePABridgeChatHandler(ctx, ChatWiringDeps{DataDir: deps.DataDir, Events: deps.Events}, module, stores.Binding, subject, deps.Clock)
//     if err != nil { return nil, closeStateOnError(state, err) }
//     _ = chatBridge
//   (NewTelegramBridge's own constructor performs the RegisterHandler call).
//
// Inputs: the daemon's data directory (cascade.db lives at DataDir/cascade.db,
//   the SAME path cmd/cascade/chat_wiring.go opens — see that file's own
//   header for why a second sqlite connection to it is this tree's
//   established pattern, not a new one), the bridge's BridgeEventPublisher,
//   the already-constructed *telegram.TelegramModule/BindingStore/subject,
//   and a clock.
//
// Outputs: a *telegram.TelegramBridge whose HandlerText is already wired.
//
// Constraints:
//   - NO NEW RPC METHOD. internal/conversation/adapter.go registers
//     chat.append_turn/get_thread/list_threads/search only (S-43.T2); there
//     is no wire method for ThreadPrivacy. Adding one is that ticket's
//     files_scope, not this one's, so bridgeThreadPrivacyResolver reads it
//     the way registerContextEngineHandlers/wireConductorExpand already do
//     for their own namespaces: a second connection to the same cascade.db.
//   - THE CHAT SERVICE REUSES cascadePAClient'S OWN WIRE CODE.
//     bridgeChatService is the identical chat.append_turn round trip
//     cascadePAClient.OneShot (cascadepa_wiring.go) makes for `cascade
//     chat`, over the SAME appendTurnParams/rpcDoer/pathResolver types —
//     one RPC client shape, not two.
//
// SPORT: internal/plugins:cascadepa-bridge-chat-wiring (ADD) — P1-E23-W5-S48-T2.

// ChatWiringDeps is what NewCascadePABridgeChatHandler needs beyond the
// already-constructed module/binding/subject.
type ChatWiringDeps struct {
	// DataDir is the daemon's data directory (matches BridgeDeps.DataDir).
	DataDir string
	// Events is the bridge's journal bus (matches BridgeDeps.Events).
	Events BridgeEventPublisher
}

// bridgeChatService adapts internal/client's unix-socket JSON-RPC transport
// to telegram.ChatService, over the SAME appendTurnParams/rpcDoer shapes
// cascadePAClient uses for `cascade chat`'s identical round trip.
type bridgeChatService struct {
	dial         client.DialFunc
	timeout      time.Duration
	resolvePaths pathResolver
	// doer, when non-nil, replaces the real rpcClient() construction --
	// see rpcDoer's own doc comment (cascadepa_wiring.go). Always nil in
	// production; only a same-package test ever sets it.
	doer rpcDoer
}

func newBridgeChatService() bridgeChatService {
	return bridgeChatService{dial: client.UnixDialer, timeout: cascadePAClientTimeout, resolvePaths: runtime.NewDefaultPathProvider}
}

func (c bridgeChatService) rpcClient() (rpcDoer, error) {
	if c.doer != nil {
		return c.doer, nil
	}
	paths, err := c.resolvePaths()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "cascade-pa bridge: resolve daemon socket path")
	}
	return client.New(paths.SocketPath(), c.dial, c.timeout), nil
}

// AppendTurn implements telegram.ChatService over the real chat.append_turn
// RPC (internal/conversation.MethodAppendTurn, S-43.T2's adapter).
func (c bridgeChatService) AppendTurn(ctx context.Context, threadID, role, text string) (string, error) {
	rpc, err := c.rpcClient()
	if err != nil {
		return "", err
	}
	params := appendTurnParams{ThreadID: threadID, Role: role,
		Segments: []appendSegmentWire{{Kind: "text", Content: text}}}
	var result appendTurnResult
	if err := rpc.Do(ctx, conversation.MethodAppendTurn, params, &result); err != nil {
		return "", err
	}
	return result.TurnID, nil
}

var _ telegram.ChatService = bridgeChatService{}

// bridgeThreadPrivacyResolver resolves a bridge thread's §5.16 tier by
// reading conversation_thread_privacy over its own sqlite connection to
// cascade.db — see this file's header for why no RPC method exists yet.
type bridgeThreadPrivacyResolver struct {
	store conversation.Store
}

func newBridgeThreadPrivacyResolver(
	ctx context.Context, dataDir string, clock migrate.Clock,
) (bridgeThreadPrivacyResolver, error) {
	dbPath := filepath.Join(dataDir, "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		return bridgeThreadPrivacyResolver{}, cascade.Wrap(cascade.KindUnavailable, err, "cascade-pa bridge: open cascade.db")
	}
	if err := conversation.ApplyConversationSchema(
		ctx, db, migrate.SQLiteEmitter{}, clock, dbPath, filepath.Join(dataDir, "backups")); err != nil {
		_ = db.Close()
		return bridgeThreadPrivacyResolver{}, err
	}
	return bridgeThreadPrivacyResolver{store: conversation.NewStore(db)}, nil
}

// ThreadPrivacy implements telegram.ThreadPrivacyResolver over the real
// conversation.Store.ThreadPrivacy (S-44.T2), cast through its own
// String() so the plugin-facing tier is exactly the store's value, never a
// re-derived comparison.
func (r bridgeThreadPrivacyResolver) ThreadPrivacy(ctx context.Context, threadID string) (cascadepa.SensitivityTier, error) {
	tier, err := r.store.ThreadPrivacy(ctx, threadID)
	if err != nil {
		return "", err
	}
	return cascadepa.SensitivityTier(tier.String()), nil
}

var _ telegram.ThreadPrivacyResolver = bridgeThreadPrivacyResolver{}

// bridgeRefusedPayload is EventKindBridgeRefused's wire shape: the exact
// four fields the contract names, never message content.
type bridgeRefusedPayload struct {
	ThreadID      string `json:"thread_id"`
	ResolvedTier  string `json:"resolved_tier"`
	Reason        string `json:"reason"`
	CorrelationID string `json:"correlation_id"`
}

// EventKindBridgeRefused records a W/S-48.T2 chat-parity refusal —
// sibling to cascadepa_bridge_events.go's own EventKindBridge* constants,
// declared here because that file is under concurrent edit (see header).
const EventKindBridgeRefused events.EventKind = "bridge.refused"

// bridgeDivergenceJournal publishes bridge.refused events over the SAME
// BridgeEventPublisher the S-48.T1/T3 journal records lockouts/quarantines
// through (bridgeEventNamespace/bridgeEventSource, cascadepa_bridge_events.go).
type bridgeDivergenceJournal struct {
	bus BridgeEventPublisher
}

// marshalBridgeRefusedPayload is json.Marshal, indirected so a test can
// drive EmitRefused's error branch — mirroring cascadepa_bridge_events.go's
// identical marshalQuarantineEvent precedent: bridgeRefusedPayload's real
// fields are plain strings, so a real populated value can never fail to
// encode, and that branch would otherwise be permanently dead code.
var marshalBridgeRefusedPayload = json.Marshal

// EmitRefused implements telegram.DivergenceSink. A publish failure never
// changes a decision (the refusal already happened) — mirroring
// bridgeJournal.publish's identical rule.
func (j bridgeDivergenceJournal) EmitRefused(ctx context.Context, e telegram.RefusalEvent) {
	if j.bus == nil {
		return
	}
	payload, err := marshalBridgeRefusedPayload(bridgeRefusedPayload{
		ThreadID: e.ThreadID, ResolvedTier: e.ResolvedTier, Reason: e.Reason, CorrelationID: e.CorrelationID,
	})
	if err != nil {
		return
	}
	_, _ = j.bus.Publish(context.WithoutCancel(ctx), bridgeEventNamespace, EventKindBridgeRefused, bridgeEventSource, payload)
}

var _ telegram.DivergenceSink = bridgeDivergenceJournal{}

// NewCascadePABridgeChatHandler assembles the chat-parity ChatBridge for
// module and registers it as HandlerText (telegram.NewTelegramBridge's own
// constructor performs that call) — the real, non-test production caller
// NewTelegramBridge needs.
func NewCascadePABridgeChatHandler(ctx context.Context, deps ChatWiringDeps,
	module *telegram.TelegramModule, binding *cascadepa.BindingStore, subject string, clock migrate.Clock,
) (*telegram.TelegramBridge, error) {
	if deps.DataDir == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "cascade-pa bridge: chat wiring requires a data directory")
	}
	privacy, err := newBridgeThreadPrivacyResolver(ctx, deps.DataDir, clock)
	if err != nil {
		return nil, err
	}
	chat := newBridgeChatService()
	divergence := bridgeDivergenceJournal{bus: deps.Events}
	return telegram.NewTelegramBridge(module, binding, subject, chat, privacy, divergence), nil
}
