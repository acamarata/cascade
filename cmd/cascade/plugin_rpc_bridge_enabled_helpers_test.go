// Purpose: fixtures for the ENABLED-bridge composition tests
//
//	(plugin_rpc_bridge_enabled_test.go): a real enabled cascade-pa bridge
//	mounted through mountCascadePABridge over a throwaway data directory — a
//	file-only vault holding a synthetic token under a standing grant, the
//	module manifest turned on, a paired binding and an internal-tier thread
//	written through the real internal/bridge and internal/conversation
//	stores — with a recording Bot API transport (deps.Transport) standing in
//	for api.telegram.org.
//
// Constraints: NO KEYCHAIN (ForceFileVault plus a failing Runner), NO REAL
//
//	HOME (every path under t.TempDir()), NO NETWORK (the transport answers
//	from the sendmessage_ok.json fixture, whose provenance is recorded in
//	plugins/cascade-pa/telegram/testdata/README.md; getUpdates parks until
//	its context ends).
//
// SPORT: cmd/cascade plugin_rpc_bridge_enabled/TEST (ADD) — fix18 round 3.
package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/bridge"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

// Synthetic identities: obviously not a real bot token or Telegram chat.
const (
	enabledBridgeToken  = "222222:SYNTHETIC-CMD-TEST-TOKEN"
	enabledBridgeChat   = "555000111"
	enabledBridgeThread = "bridge-telegram:" + enabledBridgeChat
)

// botTransport is the recording Bot API transport. sendMessage bodies are
// recorded; when hold is non-nil every sendMessage parks until hold closes.
type botTransport struct {
	mu      sync.Mutex
	sends   []string
	arrived chan struct{}
	hold    chan struct{}
	reply   []byte
}

func (b *botTransport) Post(ctx context.Context, url string, body []byte) ([]byte, error) {
	switch {
	case strings.HasSuffix(url, "/"+telegram.MethodGetUpdates):
		<-ctx.Done()
		return nil, ctx.Err()
	case strings.HasSuffix(url, "/"+telegram.MethodSendMessage):
		b.mu.Lock()
		b.sends = append(b.sends, string(body))
		b.mu.Unlock()
		select {
		case b.arrived <- struct{}{}:
		default:
		}
		if b.hold != nil {
			select {
			case <-b.hold:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return b.reply, nil
	}
	return nil, errors.New("botTransport: unexpected Bot API method")
}

func (b *botTransport) sendCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.sends)
}

// enabledBridgeEnv is one mounted enabled bridge.
type enabledBridgeEnv struct {
	deps      plugins.BridgeDeps
	bus       *events.Bus
	transport *botTransport
}

// newEnabledBridgeEnv prepares a data directory an enabled bridge will
// assemble from; the caller mounts it.
func newEnabledBridgeEnv(t *testing.T) *enabledBridgeEnv {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	manifest := filepath.Join(dataDir, "plugins", "cascade-pa", "manifest.toml")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o700); err != nil {
		t.Fatalf("mkdir manifest dir: %v", err)
	}
	if err := os.WriteFile(manifest, []byte("[modules.telegram]\nenabled = true\n"), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	vault := secrets.Config{Service: "cascade-bridge-cmd-test", Dir: dataDir, ForceFileVault: true,
		Runner: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("test: no keychain") }}
	grantBridgeToken(t, vault, dataDir)
	seedBoundInternalThread(t, dataDir)
	reply, err := os.ReadFile(filepath.Join("..", "..", "plugins", "cascade-pa", "telegram", "testdata", "sendmessage_ok.json"))
	if err != nil {
		t.Fatalf("read sendMessage fixture: %v", err)
	}
	clock := runtime.NewSystemClock()
	env := &enabledBridgeEnv{
		bus:       events.New(storetest.NewMemStore(), clock),
		transport: &botTransport{arrived: make(chan struct{}, 8), reply: reply},
	}
	env.deps = plugins.BridgeDeps{DataDir: dataDir, Vault: vault, Clock: clock, Events: env.bus, Transport: env.transport}
	return env
}

// grantBridgeToken stores the synthetic token in the file vault under the
// bridge's default key, with the standing grant the bridge requires.
func grantBridgeToken(t *testing.T, vault secrets.Config, dataDir string) {
	t.Helper()
	const key = "cascade-pa.telegram.bot_token"
	custody, err := secrets.SelectCustody(vault)
	if err != nil {
		t.Fatalf("SelectCustody: %v", err)
	}
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	if _, err := broker.Set(context.Background(), key, []byte(enabledBridgeToken), secrets.SetUpdate); err != nil {
		t.Fatalf("vault Set: %v", err)
	}
	store, err := secrets.NewFileGrantStore(dataDir)
	if err != nil {
		t.Fatalf("NewFileGrantStore: %v", err)
	}
	grants, err := secrets.NewGrants(store, runtime.NewSystemClock())
	if err != nil {
		t.Fatalf("NewGrants: %v", err)
	}
	if _, err := grants.Issue(context.Background(), key, 10*time.Minute); err != nil {
		t.Fatalf("grant Issue: %v", err)
	}
}

// seedBoundInternalThread pairs enabledBridgeChat to the bot's subject and
// marks its thread internal, through the real stores the daemon reads.
func seedBoundInternalThread(t *testing.T, dataDir string) {
	t.Helper()
	ctx, clock := context.Background(), runtime.NewSystemClock()
	dbPath := filepath.Join(dataDir, "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open cascade.db: %v", err)
	}
	defer func() { _ = db.Close() }()
	backups := filepath.Join(dataDir, "backups")
	if err := bridge.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, dbPath, backups); err != nil {
		t.Fatalf("bridge schema: %v", err)
	}
	if err := bridge.NewStore(db).Save(ctx, bridge.SubjectRow{Subject: telegram.SubjectFromToken(enabledBridgeToken),
		TrustTier: "paired-device", PairedAt: clock.Now(), AllowedFrom: []string{enabledBridgeChat}}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if err := conversation.ApplyConversationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, dbPath, backups); err != nil {
		t.Fatalf("conversation schema: %v", err)
	}
	if err := conversation.NewStore(db).SetThreadPrivacy(ctx, enabledBridgeThread, provider.SensitivityInternal); err != nil {
		t.Fatalf("SetThreadPrivacy: %v", err)
	}
	seedEnabledBridgeMap(t, db, dataDir, clock)
}

// seedEnabledBridgeMap binds the paired chat to enabledBridgeThread in the
// durable chat-to-thread map, as the owner's first message would have:
// bridge.send resolves a thread's chat through that map only.
func seedEnabledBridgeMap(t *testing.T, db *sql.DB, dataDir string, clock runtime.Clock) {
	t.Helper()
	ctx := context.Background()
	threads, err := plugins.OpenBridgeThreadMap(ctx, db, dataDir, clock)
	if err != nil {
		t.Fatalf("OpenBridgeThreadMap: %v", err)
	}
	chat, err := strconv.ParseInt(enabledBridgeChat, 10, 64)
	if err == nil {
		_, err = threads.Bind(ctx, telegram.SubjectFromToken(enabledBridgeToken), chat, enabledBridgeThread)
	}
	if err != nil {
		t.Fatalf("seed map row: %v", err)
	}
}
