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
// Inputs: the daemon's data directory (cascade.db lives at DataDir/cascade.db;
//   ThreadPrivacy still applies its own migration set the way
//   registerContextEngineHandlers/wireConductorExpand and
//   cmd/cascade/chat_wiring.go's wireChatHandlers apply theirs — a namespace
//   of its own on the shared file, not a shared table), the bridge's
//   BridgeEventPublisher, the OWNING *sql.DB connection openBridgeState
//   already opened (ChatWiringDeps.DB — see ci-fix14 below for why this is a
//   parameter, not a second sql.Open), the already-constructed
//   *telegram.TelegramModule/BindingStore/subject, and a clock.
//
// Outputs: a *telegram.TelegramBridge whose HandlerText is already wired.
//
// Constraints:
//   - NO NEW RPC METHOD. internal/conversation/adapter.go registers
//     chat.append_turn/get_thread/list_threads/search only (S-43.T2); there
//     is no wire method for ThreadPrivacy. Adding one is that ticket's
//     files_scope, not this one's, so bridgeThreadPrivacyResolver reads it
//     directly over conversation.Store instead.
//   - THE CHAT SERVICE REUSES cascadePAClient'S OWN WIRE CODE.
//     bridgeChatService is the identical chat.append_turn round trip
//     cascadePAClient.OneShot (cascadepa_wiring.go) makes for `cascade
//     chat`, over the SAME appendTurnParams/rpcDoer/pathResolver types —
//     one RPC client shape, not two.
//   - ONE CONNECTION, ONE OWNER (ci-fix14, 2026-09-22). This file used to
//     open its OWN second *sql.DB to cascade.db inside
//     newBridgeThreadPrivacyResolver and never closed it: nothing in
//     enabledBridge or BridgeRuntime.Stop held a reference to it, so every
//     bridge construction leaked a sqlite handle for the life of the
//     process. On POSIX that leak was invisible (an idle connection does
//     not block anything); on windows/amd64 CI it made every bridge test's
//     t.TempDir() cleanup fail with "The process cannot access the file
//     because it is being used by another process" on cascade.db. The fix
//     is not a shorter TempDir path — it is that this file no longer owns
//     any connection at all: ChatWiringDeps.DB is the SAME *sql.DB
//     enabledBridge's openBridgeState already opened (bridgeStateSQLDB,
//     cascadepa_bridge_state.go), so the ONE close in
//     BridgeRuntime.Stop (closeStateAfterStop, cascadepa_bridge_state_close.go)
//     already releases it. A caller that is not enabledBridge (a test
//     exercising this file directly) opens and t.Cleanup-closes its own
//     *sql.DB — see cascadepa_bridge_fixtures_test.go's
//     openTestConversationDB.
//
// SPORT: internal/plugins:cascadepa-bridge-chat-wiring (ADD) — P1-E23-W5-S48-T2; ci-fix14.

// ChatWiringDeps is what NewCascadePABridgeChatHandler needs beyond the
// already-constructed module/binding/subject.
type ChatWiringDeps struct {
	// DataDir is the daemon's data directory (matches BridgeDeps.DataDir).
	// Still required with DB set: ApplyConversationSchema's dbPath/backupDir
	// arguments are derived from it.
	DataDir string
	// DB is the OWNING *sql.DB connection to DataDir/cascade.db — in
	// production, the SAME connection enabledBridge's openBridgeState
	// already opened (bridgeStateSQLDB). This constructor applies the
	// conversation schema on it but never opens or closes it: ownership,
	// and the eventual Close, belong entirely to the caller (see this
	// file's header, ci-fix14). Required.
	DB *sql.DB
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
// reading conversation_thread_privacy over cascade.db — see this file's
// header for why no RPC method exists yet, and for why db is a connection
// this type borrows rather than owns.
type bridgeThreadPrivacyResolver struct {
	store conversation.Store
}

// newBridgeThreadPrivacyResolver applies the conversation schema onto db —
// an ALREADY-OPEN connection the caller owns (production: enabledBridge's
// state.sqlDB(); a direct test: its own t.Cleanup-closed handle, see
// openTestConversationDB) — and wraps it in a conversation.Store. It never
// opens or closes db itself (ci-fix14: the previous version did both, and
// closed it on nothing but its own schema-apply error, which is the leak
// windows/amd64 CI's TempDir cleanup caught).
func newBridgeThreadPrivacyResolver(
	ctx context.Context, db *sql.DB, dataDir string, clock migrate.Clock,
) (bridgeThreadPrivacyResolver, error) {
	if db == nil {
		return bridgeThreadPrivacyResolver{}, cascade.New(cascade.KindInvalidInput,
			"cascade-pa bridge: chat wiring requires a reusable sqlite connection")
	}
	dbPath := filepath.Join(dataDir, "cascade.db")
	if err := conversation.ApplyConversationSchema(
		ctx, db, migrate.SQLiteEmitter{}, clock, dbPath, filepath.Join(dataDir, "backups")); err != nil {
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
	privacy, err := newBridgeThreadPrivacyResolver(ctx, deps.DB, deps.DataDir, clock)
	if err != nil {
		return nil, err
	}
	chat := newBridgeChatService()
	divergence := bridgeDivergenceJournal{bus: deps.Events}
	return telegram.NewTelegramBridge(module, binding, subject, chat, privacy, divergence), nil
}
