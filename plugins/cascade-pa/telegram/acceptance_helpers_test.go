// Purpose (this file): the real-component rig the P1-CPA-14 acceptance
//   tests drive: the REAL TelegramModule/TelegramBridge dispatch, the REAL
//   egress engine (EgressClassBridge), a REAL detector-backed secret
//   scanner, the REAL conversation ThreadPrivacy store and the REAL
//   P1-CPA-02 chat-to-thread map. No test-side copy of any gate.
//
// Inputs: this package's rig primitives (newMemState, okRegistrar, fakeDoer,
//   textUpdate, fixedTestClock, t0, mintingChat, fakeDivergenceSink), a
//   throwaway file vault (internal/secrets) and a throwaway sqlite file
//   (internal/conversation) per rig.
//
// Constraints: the three adapters below hold no decision of their own. Each
//   reproduces, line for line, the production adapter in internal/plugins
//   (bridgeEgressGate, bridgeSecretScanner, bridgeThreadPrivacyResolver),
//   which this package cannot import: internal/plugins imports it back.
//   The wire stays a recording fakeDoer (no socket in an untagged test).
//
// SPORT: plugins/cascade-pa/telegram acceptance-fixtures/TEST (P1-CPA-14).

package telegram

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// realAcceptEgressGate is internal/plugins.bridgeEgressGate, verbatim.
type realAcceptEgressGate struct{ engine *egress.Engine }

func (g realAcceptEgressGate) Guard(
	ctx context.Context, tier cascadepa.SensitivityTier, content []byte,
) ([]byte, error) {
	if g.engine == nil {
		return nil, cascade.New(cascade.KindUnavailable,
			"cascade-pa bridge: no egress engine is configured; refusing to write")
	}
	return g.engine.InterceptClass(ctx, egress.EgressClassBridge, egress.SensitivityTier(tier), content)
}

// realAcceptScanner is internal/plugins.bridgeSecretScanner, verbatim.
type realAcceptScanner struct{ d *secrets.Detector }

func (s realAcceptScanner) Scan(content []byte) SecretScanOutcome {
	hits := s.d.ScanCertain(content)
	if len(hits) == 0 {
		return SecretScanOutcome{}
	}
	return SecretScanOutcome{Detected: true, Class: string(hits[0].Class)}
}

// realAcceptThreadPrivacy is internal/plugins.bridgeThreadPrivacyResolver's
// ThreadPrivacy, verbatim, over the real conversation.Store.
type realAcceptThreadPrivacy struct{ store conversation.Store }

func (r realAcceptThreadPrivacy) ThreadPrivacy(ctx context.Context, threadID string) (cascadepa.SensitivityTier, error) {
	tier, err := r.store.ThreadPrivacy(ctx, threadID)
	if err != nil {
		return cascadepa.TierLocalOnly, err
	}
	return tier, nil
}

// acceptNoKeychain forces every custody selection onto the file vault.
type acceptNoKeychain struct{}

func (acceptNoKeychain) Error() string {
	return "test: no platform keychain helper in acceptance tests"
}

// acceptRealFirewall builds a real detector and a real egress engine over a
// file vault under dir, storing storedValue under storedName when non-empty.
func acceptRealFirewall(t *testing.T, dir, storedName, storedValue string) (*secrets.Detector, *egress.Engine) {
	t.Helper()
	custody, err := secrets.SelectCustody(secrets.Config{
		Service: "cascade-pa-bridge-acceptance-test", Dir: dir, ForceFileVault: true,
		Runner: func(context.Context, string, ...string) ([]byte, error) { return nil, acceptNoKeychain{} },
	})
	if err != nil {
		t.Fatalf("SelectCustody: %v", err)
	}
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	if storedName != "" {
		if _, serr := broker.Set(context.Background(), storedName, []byte(storedValue), secrets.SetUpdate); serr != nil {
			t.Fatalf("Set: %v", serr)
		}
	}
	vault, err := secrets.NewEgressVault(broker)
	if err != nil {
		t.Fatalf("NewEgressVault: %v", err)
	}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	engine, err := egress.NewEngine(egress.DefaultRegistry(), vault, detector)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return detector, engine
}

// acceptConversationStore opens a fresh sqlite file under t.TempDir(),
// applies the real conversation schema and returns the real store.
func acceptConversationStore(t *testing.T) conversation.Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := conversation.ApplyConversationSchema(context.Background(), db, migrate.SQLiteEmitter{},
		fixedTestClock{t0()}, dbPath, filepath.Join(dir, "backups")); err != nil {
		t.Fatalf("ApplyConversationSchema: %v", err)
	}
	return conversation.NewStore(db)
}

// acceptRig is one bridge over the real seams plus its recorders.
type acceptRig struct {
	module     *TelegramModule
	doer       *fakeDoer
	stores     *cascadepa.Stores
	bridge     *TelegramBridge
	chat       *mintingChat
	divergence *fakeDivergenceSink
	threads    ThreadMap
	privacy    conversation.Store
	quarantine *recordingQuarantineSink
}

// newAcceptRig builds the rig; storedName/storedValue seed the vault the
// egress substitution pass resolves against ("" stores nothing).
func newAcceptRig(t *testing.T, storedName, storedValue string) *acceptRig {
	t.Helper()
	detector, engine := acceptRealFirewall(t, t.TempDir(), storedName, storedValue)
	clock := fixedTestClock{t0()}
	stores := cascadepa.NewStores(clock, &okRegistrar{}, newMemState(), testPairKey(t))
	r := &acceptRig{doer: &fakeDoer{}, stores: stores, chat: &mintingChat{fakeChatService: &fakeChatService{}},
		divergence: &fakeDivergenceSink{}, privacy: acceptConversationStore(t), quarantine: &recordingQuarantineSink{}}
	client := NewBotClient(testSubject, r.doer, realAcceptEgressGate{engine: engine}, stores.Updates)
	client.sleep, _ = instantSleep()
	pairer := newPairCoordinator(stores.Pairing, stores.Binding, &recordingSink{}, clock)
	r.module = NewTelegramModule(testSubject, client, stores.Binding, pairer, nodeVerbPolicy(), clock)
	r.module.secretScanner = realAcceptScanner{d: detector}
	r.module.quarantine = r.quarantine
	r.threads = NewThreadMap(cascadepa.NewThreadMapStore(newMemState()), stores.Binding, testSubject)
	r.bridge = NewTelegramBridge(r.module, stores.Binding, testSubject, BridgeSeams{
		Chat: r.chat, Privacy: realAcceptThreadPrivacy{store: r.privacy}, Divergence: r.divergence,
		Threads: r.threads, Legacy: &fakeLegacyThreads{exists: false},
	})
	return r
}

// pairOwner binds owner as the paired sender, mints its thread through
// chat.create_thread, binds the owner DM to it through the real map and
// marks the thread's privacy in the real store. It returns the thread id.
func (r *acceptRig) pairOwner(t *testing.T, owner int64, tier provider.SensitivityTier) string {
	t.Helper()
	ctx := context.Background()
	if _, err := r.stores.Binding.Bind(ctx, testSubject, strconv.FormatInt(owner, 10)); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	minted, err := r.chat.CreateThread(ctx, bridgeThreadTier)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	threadID, err := r.threads.Bind(ctx, Chat{ID: owner, Type: "private"}, owner, minted)
	if err != nil {
		t.Fatalf("ThreadMap.Bind: %v", err)
	}
	if err := r.privacy.SetThreadPrivacy(ctx, threadID, tier); err != nil {
		t.Fatalf("SetThreadPrivacy: %v", err)
	}
	return threadID
}

// sendMessages returns every recorded sendMessage body, in order.
func (r *acceptRig) sendMessages() []sendMessageParams {
	r.doer.mu.Lock()
	defer r.doer.mu.Unlock()
	var out []sendMessageParams
	for _, c := range r.doer.calls {
		if c.method != MethodSendMessage {
			continue
		}
		if p, ok := c.params.(sendMessageParams); ok {
			out = append(out, p)
		}
	}
	return out
}

// acceptWitness is a synthetic credential-shaped witness, assembled at run
// time so no tracked literal has the credential shape.
func acceptWitness() string { return "AK" + "IA" + "ABCDEFGHIJKLMNOP" }
