// Purpose (this file): unit coverage for cascadepa_bridge_chat_wiring.go's
//   three seams — bridgeChatService (real transport-failure classification,
//   Art.7.2-safe via client.UnixDialer against a socket nothing binds,
//   mirroring cascadepa_wiring_test.go's identical pattern),
//   bridgeThreadPrivacyResolver (a REAL conversation.Store round trip over
//   a real sqlite file, Art.2), and bridgeDivergenceJournal (over the
//   recordingBus fake cascadepa_bridge_events_test.go already declares) —
//   plus NewCascadePABridgeChatHandler's own construction, the real,
//   non-test production caller of telegram.NewTelegramBridge.
//
// SPORT: internal/plugins:cascadepa-bridge-chat-wiring (TEST) — P1-E23-W5-S48-T2.

package plugins

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

// bridgeTestClock is a fixed migrate.Clock/cascadepa.PairClock, mirroring
// domain_test.go's fakeClock (Art.7.3: no bare time.Now in test setup).
type bridgeTestClock struct{ t time.Time }

func (c bridgeTestClock) Now() time.Time { return c.t }

func newBridgeTestClock() bridgeTestClock {
	return bridgeTestClock{t: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)}
}

func TestBridgeChatService_AppendTurn_TransportUnreachable(t *testing.T) {
	socket := missingSocketPath(t)
	c := bridgeChatService{dial: client.UnixDialer, timeout: time.Second,
		resolvePaths: func() (runtime.PathProvider, error) { return fakePathProvider{socket: socket}, nil }}
	_, err := c.AppendTurn(context.Background(), "t1", "user", "hi")
	if err == nil {
		t.Fatal("AppendTurn: err = nil, want a transport error")
	}
	if got := err.Error(); !containsAll(got, "chat.append_turn", "daemon not running or unreachable") {
		t.Errorf("AppendTurn: err = %q, want internal/client's own classified transport error", got)
	}
}

func TestBridgeChatService_AppendTurn_PathResolutionFailure(t *testing.T) {
	wantErr := cascade.New(cascade.KindUnavailable, "boom: no home directory")
	c := bridgeChatService{resolvePaths: func() (runtime.PathProvider, error) { return nil, wantErr }}
	_, err := c.AppendTurn(context.Background(), "t1", "user", "hi")
	if err == nil || !containsAll(err.Error(), "boom: no home directory") {
		t.Fatalf("AppendTurn: err = %v, want it to name the path-resolution failure", err)
	}
}

// TestBridgeChatService_AppendTurn_Success drives AppendTurn's production
// happy path (cascadepa_bridge_chat_wiring.go:108, `return result.TurnID,
// nil`) through the same fakeRPCDoer seam cascadepa_rpc_success_test.go
// declares for every other adapter's success mapping — internal/plugins
// cannot reach this branch through a real socket (see that file's own doc
// comment on why).
func TestBridgeChatService_AppendTurn_Success(t *testing.T) {
	doer := &fakeRPCDoer{fill: func(out any) {
		res := out.(*appendTurnResult)
		*res = appendTurnResult{ThreadID: "t1", TurnID: "turn-1", Seq: 3}
	}}
	c := bridgeChatService{doer: doer}

	turnID, err := c.AppendTurn(context.Background(), "t1", "user", "hi")
	if err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if turnID != "turn-1" {
		t.Fatalf("AppendTurn turnID = %q, want %q", turnID, "turn-1")
	}
	if doer.calledMethod != conversation.MethodAppendTurn {
		t.Fatalf("AppendTurn called %q, want %q", doer.calledMethod, conversation.MethodAppendTurn)
	}
}

var _ telegram.ChatService = bridgeChatService{}

// TestBridgeThreadPrivacyResolver_RealStoreRoundTrip is Art.2's real
// counterpart: a real conversation.Store, over a real sqlite file under
// t.TempDir(), set through Store.SetThreadPrivacy and read back through
// THIS ticket's own resolver — proving the cast is exact, not a
// translation table that could disagree (egress.go's own doc comment).
func TestBridgeThreadPrivacyResolver_RealStoreRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	ctx := context.Background()
	clock := newBridgeTestClock()

	db := openTestConversationDB(t, dataDir)
	resolver, err := newBridgeThreadPrivacyResolver(ctx, db, dataDir, clock)
	if err != nil {
		t.Fatalf("newBridgeThreadPrivacyResolver: %v", err)
	}
	if err := resolver.store.SetThreadPrivacy(ctx, "t-internal", provider.SensitivityInternal); err != nil {
		t.Fatalf("SetThreadPrivacy: %v", err)
	}

	tier, err := resolver.ThreadPrivacy(ctx, "t-internal")
	if err != nil {
		t.Fatalf("ThreadPrivacy: %v", err)
	}
	if tier != cascadepa.TierInternal {
		t.Fatalf("ThreadPrivacy(t-internal) = %q, want %q", tier, cascadepa.TierInternal)
	}

	// Absence reads restricted (privacy.go's own fail-closed contract),
	// proven through THIS resolver, not asserted about the store alone.
	tier, err = resolver.ThreadPrivacy(ctx, "never-set")
	if err != nil {
		t.Fatalf("ThreadPrivacy(never-set): %v", err)
	}
	if tier != cascadepa.TierRestricted {
		t.Fatalf("ThreadPrivacy(never-set) = %q, want %q (absence means restricted)", tier, cascadepa.TierRestricted)
	}
}

func TestBridgeThreadPrivacyResolver_UnwritableDataDirRefuses(t *testing.T) {
	bad := "/nonexistent-cascade-test-dir/sub"
	db := openTestConversationDB(t, t.TempDir())
	if _, err := newBridgeThreadPrivacyResolver(context.Background(), db, bad, newBridgeTestClock()); err == nil {
		t.Fatal("newBridgeThreadPrivacyResolver over an unwritable directory succeeded")
	}
}

// TestBridgeThreadPrivacyResolver_NilDBRefuses proves the contract: a nil
// connection is always refused with KindInvalidInput, never silently
// accepted. newBridgeThreadPrivacyResolver's own early guard is the FIRST
// line of defense (fails fast, names the bridge-specific requirement);
// ApplyConversationSchema (internal/conversation/domain.go) also refuses a
// nil db on its own, so this test verifies the caller-visible contract
// rather than which of the two layers fires — removing the early guard
// alone does not turn this red, since the fallback still refuses with the
// same Kind (verified during ci-fix14's mutation pass).
func TestBridgeThreadPrivacyResolver_NilDBRefuses(t *testing.T) {
	_, err := newBridgeThreadPrivacyResolver(context.Background(), nil, t.TempDir(), newBridgeTestClock())
	if err == nil {
		t.Fatal("newBridgeThreadPrivacyResolver with a nil db succeeded")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("newBridgeThreadPrivacyResolver err = %v, want KindInvalidInput", err)
	}
}

// TestBridgeThreadPrivacyResolver_StoreErrorPropagates drives ThreadPrivacy's
// error branch (cascadepa_bridge_chat_wiring.go:142-144) through a REAL
// conversation.Store wrapping an already-closed *sql.DB, Art.2's real
// counterpart rather than a mocked store.
func TestBridgeThreadPrivacyResolver_StoreErrorPropagates(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}
	resolver := bridgeThreadPrivacyResolver{store: conversation.NewStore(db)}

	if _, err := resolver.ThreadPrivacy(context.Background(), "t1"); err == nil {
		t.Fatal("ThreadPrivacy over a closed store succeeded, want an error")
	}
}

func TestBridgeDivergenceJournal_EmitRefused_PublishesOverTheBus(t *testing.T) {
	bus := newRecordingBus()
	j := bridgeDivergenceJournal{bus: bus}
	j.EmitRefused(context.Background(), telegram.RefusalEvent{
		ThreadID: "bridge-telegram:1", ResolvedTier: "local-only",
		Reason: "thread is local-only, cannot bridge", CorrelationID: "chat:1",
	})
	got := bus.events()
	if len(got) != 1 || got[0].kind != EventKindBridgeRefused || got[0].namespace != bridgeEventNamespace {
		t.Fatalf("published %+v, want one bridge.refused on %q", got, bridgeEventNamespace)
	}
	if !containsAll(got[0].payload, "cannot bridge") {
		t.Fatalf("payload %q missing the refusal reason", got[0].payload)
	}
}

func TestBridgeDivergenceJournal_NilBusIsANoop(_ *testing.T) {
	(bridgeDivergenceJournal{}).EmitRefused(context.Background(), telegram.RefusalEvent{})
}

// TestBridgeDivergenceJournal_EmitRefused_MarshalFailureIsANoop drives
// EmitRefused's error branch (cascadepa_bridge_chat_wiring.go:188-190)
// through marshalBridgeRefusedPayload's indirection — see that var's own
// doc comment for why no real bridgeRefusedPayload value can ever fail to
// encode.
func TestBridgeDivergenceJournal_EmitRefused_MarshalFailureIsANoop(t *testing.T) {
	orig := marshalBridgeRefusedPayload
	t.Cleanup(func() { marshalBridgeRefusedPayload = orig })
	wantErr := errors.New("test: marshal exploded")
	marshalBridgeRefusedPayload = func(any) ([]byte, error) { return nil, wantErr }

	bus := newRecordingBus()
	(bridgeDivergenceJournal{bus: bus}).EmitRefused(context.Background(), telegram.RefusalEvent{ThreadID: "t1"})

	if got := bus.events(); len(got) != 0 {
		t.Fatalf("EmitRefused published %v despite a marshal failure, want nothing published", got)
	}
}

var _ telegram.DivergenceSink = bridgeDivergenceJournal{}

// TestNewCascadePABridgeChatHandler_Constructs is the real, non-test
// production caller of telegram.NewTelegramBridge: it assembles a
// *telegram.TelegramModule the SAME way enabledBridge does (real state,
// real device registrar, real pairing key — cascadepa_bridge_state.go's
// and cascadepa_bridge_deps.go's own package-level constructors) and calls
// this ticket's own constructor. Registration of HandlerText is proven by
// NewTelegramBridge's own tests in the telegram package (mutation 2 there);
// this test's own job is that construction over REAL collaborators
// succeeds and the result is usable — Paired() is an assertion that can
// fail (a nil binding store, or a broken Bound() call, would either panic
// or answer true for a subject nothing ever bound).
func TestNewCascadePABridgeChatHandler_Constructs(t *testing.T) {
	deps, bus, dataDir := enabledBridgeDeps(t)
	ctx := context.Background()

	state, err := openBridgeState(ctx, dataDir)
	if err != nil {
		t.Fatalf("openBridgeState: %v", err)
	}
	closeBridgeState(t, state)
	stateDB, ok := bridgeStateSQLDB(state)
	if !ok {
		t.Fatal("bridgeStateSQLDB: state does not expose a reusable sqlite connection")
	}
	pairKey, err := cascadepa.DerivePairCodeKey(syntheticBotToken)
	if err != nil {
		t.Fatalf("DerivePairCodeKey: %v", err)
	}
	stores := cascadepa.NewStores(deps.Clock, newBridgeDeviceRegistrar(dataDir), state, pairKey)
	module := telegram.NewModule(syntheticBotToken, nil, nil, bridgeElevationPolicy{}, stores, nil, nil, nil)

	bridge, err := NewCascadePABridgeChatHandler(ctx,
		ChatWiringDeps{DataDir: dataDir, DB: stateDB, Events: bus}, module, stores.Binding, "tg-test", deps.Clock)
	if err != nil {
		t.Fatalf("NewCascadePABridgeChatHandler: %v", err)
	}
	if bridge == nil {
		t.Fatal("NewCascadePABridgeChatHandler returned a nil bridge with no error")
	}
	if bridge.Paired() {
		t.Fatal("Paired() = true for a subject nothing ever bound")
	}
}

// TestNewCascadePABridgeChatHandler_EmptyDataDirRefuses drives the
// deps.DataDir == "" guard (cascadepa_bridge_chat_wiring.go:196-198)
// directly: module/binding are never touched on this path, so nil stands
// in for them.
func TestNewCascadePABridgeChatHandler_EmptyDataDirRefuses(t *testing.T) {
	_, err := NewCascadePABridgeChatHandler(context.Background(),
		ChatWiringDeps{}, nil, nil, "tg-test", newBridgeTestClock())
	if err == nil {
		t.Fatal("NewCascadePABridgeChatHandler with an empty DataDir succeeded")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("NewCascadePABridgeChatHandler err = %v, want KindInvalidInput", err)
	}
}

// TestNewCascadePABridgeChatHandler_UnwritableDataDirPropagates proves the
// resolver-construction failure propagates through the composition root
// (cascadepa_bridge_chat_wiring.go:200-202), not just through
// newBridgeThreadPrivacyResolver's own direct test above.
func TestNewCascadePABridgeChatHandler_UnwritableDataDirPropagates(t *testing.T) {
	bad := "/nonexistent-cascade-test-dir/sub"
	db := openTestConversationDB(t, t.TempDir())
	_, err := NewCascadePABridgeChatHandler(context.Background(),
		ChatWiringDeps{DataDir: bad, DB: db}, nil, nil, "tg-test", newBridgeTestClock())
	if err == nil {
		t.Fatal("NewCascadePABridgeChatHandler over an unwritable DataDir succeeded")
	}
}
