package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/acamarata/cascade/internal/bridge"
	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

// Purpose (this file): the chat-parity composition root.
//   NewCascadePABridgeChatHandler assembles the host-mediated seams the
//   telegram package declares (ChatService, ThreadPrivacyResolver,
//   LegacyThreadLookup, DivergenceSink and the durable chat-to-thread
//   ThreadMap) and hands them to telegram.NewTelegramBridge, which registers
//   itself as the module's HandlerText. enabledBridge
//   (cascadepa_bridge_wiring.go) is the production caller.
//
// Inputs: the daemon's data directory, the OWNING *sql.DB openBridgeState
//   already opened on DataDir/cascade.db, the bridge's BridgeEventPublisher,
//   the constructed module/BindingStore/subject, and a clock.
//
// Outputs: a *telegram.TelegramBridge whose HandlerText is already wired.
//
// Constraints:
//   - THREADS COME FROM THE DAEMON. bridgeChatService mints every new bridge
//     thread through chat.create_thread over the daemon socket (owner-CLI
//     caller, so the thread is attributed "owner") and appends through
//     chat.append_turn, over the SAME rpcDoer/pathResolver shapes
//     cascadePAClient uses for `cascade chat`.
//   - THE MAP LIVES IN THE BRIDGE STATE STORE. OpenBridgeThreadMap puts the
//     chat-to-thread map on the same bridge subject table the pairing
//     binding uses (cascadepa.ThreadMapStore's create-only rows); no new
//     database file. The thread-privacy resolver and the legacy-thread
//     lookup read the conversation tables of that same cascade.db directly
//     (there is no wire method for either).
//   - ONE CONNECTION, ONE OWNER. Nothing here opens or closes a *sql.DB:
//     ChatWiringDeps.DB is the connection openBridgeState opened, closed only
//     by BridgeRuntime.Stop (closeStateAfterStop). A second, never-closed
//     handle once leaked here and broke windows CI's TempDir cleanup.
//
// SPORT: internal/plugins:cascadepa-bridge-chat-wiring (CHANGED: thread map).

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

// bridgeChatFactory builds the chat service the bridge is wired with; a
// same-package test swaps in one whose rpcDoer is a real in-process registry.
var bridgeChatFactory = newBridgeChatService

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

// createThreadWire/createThreadResultWire: chat.create_thread's wire shapes.
type createThreadWire struct {
	PrivacyMode string `json:"privacy_mode"`
}

type createThreadResultWire struct {
	ThreadID string `json:"thread_id"`
}

// CreateThread implements telegram.ChatService over the real
// chat.create_thread RPC: the daemon mints the id and writes the thread,
// its marker and its attribution in one transaction.
func (c bridgeChatService) CreateThread(ctx context.Context, privacyMode string) (string, error) {
	rpc, err := c.rpcClient()
	if err != nil {
		return "", err
	}
	var result createThreadResultWire
	err = rpc.Do(ctx, conversation.MethodCreateThread, createThreadWire{PrivacyMode: privacyMode}, &result)
	if err == nil && result.ThreadID == "" {
		err = cascade.New(cascade.KindIntegrity, "cascade-pa bridge: chat.create_thread returned no thread id")
	}
	return result.ThreadID, err
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
		return cascadepa.TierLocalOnly, err
	}
	return tier, nil
}

var _ telegram.ThreadPrivacyResolver = bridgeThreadPrivacyResolver{}

// ThreadExists implements telegram.LegacyThreadLookup over the same
// conversation.Store: whether a chat's legacy thread already holds a row.
func (r bridgeThreadPrivacyResolver) ThreadExists(ctx context.Context, threadID string) (bool, error) {
	_, ok, err := r.store.GetThread(ctx, threadID)
	return ok && err == nil, err
}

var _ telegram.LegacyThreadLookup = bridgeThreadPrivacyResolver{}

// OpenBridgeThreadMap returns the durable chat-to-thread map on db (the
// bridge's cascade.db connection, owned by the caller): it applies the
// bridge schema (idempotent; enabledBridge's openBridgeState already did)
// and puts cascadepa.ThreadMapStore on the bridge subject table.
func OpenBridgeThreadMap(ctx context.Context, db *sql.DB, dataDir string, clock migrate.Clock) (*cascadepa.ThreadMapStore, error) {
	if db == nil || dataDir == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "cascade-pa bridge: the thread map needs a connection and a data directory")
	}
	dbPath := filepath.Join(dataDir, "cascade.db")
	if err := bridge.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, dbPath, filepath.Join(dataDir, "backups")); err != nil {
		return nil, err
	}
	return cascadepa.NewThreadMapStore(bridgeStateAdapter{store: bridge.NewStore(db)}), nil
}

// bridgeThreadLookup is the map lookup a ChatBridge offers bridge.send.
type bridgeThreadLookup interface {
	IsBridgeThread(ctx context.Context, threadID string) (bool, error)
}

// chatBridgeHoldsThread reports whether sender's chat-to-thread map holds
// threadID. A bridge with no map lookup holds no thread; a lookup error is
// returned so bridge.send refuses rather than guessing.
func chatBridgeHoldsThread(ctx context.Context, sender cascadepa.ChatBridge, threadID string) (bool, error) {
	lookup, ok := sender.(bridgeThreadLookup)
	if !ok {
		return false, nil
	}
	return lookup.IsBridgeThread(ctx, threadID)
}

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
	threads, err := OpenBridgeThreadMap(ctx, deps.DB, deps.DataDir, clock)
	if err != nil {
		return nil, err
	}
	return telegram.NewTelegramBridge(module, binding, subject, telegram.BridgeSeams{
		Chat: bridgeChatFactory(), Privacy: privacy, Divergence: bridgeDivergenceJournal{bus: deps.Events},
		Threads: telegram.NewThreadMap(threads, binding, subject), Legacy: privacy,
	}), nil
}
