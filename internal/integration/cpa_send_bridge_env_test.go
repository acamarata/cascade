//go:build !windows && integration

// Purpose: the daemon side of cpa_send_bridge_test.go's real-socket proof —
//
//	a real JSON-RPC registry served on a real unix socket at the path the
//	production client resolves from CASCADE_HOME, holding the real
//	chat.append_turn adapter (internal/conversation over a real modernc
//	SQLite cascade.db) and a REAL enabled cascade-pa bridge
//	(plugins.NewCascadePABridge over a file-only vault) whose Bot API
//	transport is a recording fake. The bridge's binding and the thread's
//	internal tier are written through the real internal/bridge and
//	internal/conversation stores the daemon reads.
//
// Constraints: no keychain (ForceFileVault plus a failing Runner), no real
//
//	home (CASCADE_HOME is a fresh short temp dir, short enough for darwin's
//	104-byte sun_path), loopback only (the unix socket), and no Bot API
//	traffic (sendMessage answers from plugins/cascade-pa/telegram/testdata/
//	sendmessage_ok.json, provenance in that directory's README.md).
//
// SPORT: internal/integration cpa_send_bridge (ADD) — fix18 round 3 (L2).
package integration

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/bridge"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

// Synthetic identities: obviously not a real bot token or Telegram chat.
const (
	cpaBridgeToken  = "333333:SYNTHETIC-INTEGRATION-TOKEN"
	cpaBridgeChat   = "555000111"
	cpaBridgeThread = "bridge-telegram:" + cpaBridgeChat
)

// botAPIRecorder is the recording Bot API transport (telegram.Poster).
type botAPIRecorder struct {
	mu    sync.Mutex
	sends []string
	reply []byte
}

func (b *botAPIRecorder) Post(_ context.Context, url string, body []byte) ([]byte, error) {
	if !strings.HasSuffix(url, "/"+telegram.MethodSendMessage) {
		return nil, errors.New("botAPIRecorder: only sendMessage is expected here")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sends = append(b.sends, string(body))
	return b.reply, nil
}

func (b *botAPIRecorder) sent() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.sends...)
}

// cpaDaemon is one served registry plus what the assertions read.
type cpaDaemon struct {
	api *botAPIRecorder
	bus *events.Bus
}

// startCPADaemon builds and serves the daemon side. wireBridgeSend=false
// leaves bridge.send unregistered while the bridge is still armed in this
// process — the lever that proves the mirror travels over the socket.
func startCPADaemon(t *testing.T, wireBridgeSend bool) *cpaDaemon {
	t.Helper()
	root, err := os.MkdirTemp("", "f18")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("CASCADE_HOME", root)
	t.Setenv("CASCADE_SOCKET", "")
	paths, err := runtime.NewDefaultPathProvider()
	if err != nil {
		t.Fatalf("NewDefaultPathProvider: %v", err)
	}
	clock := runtime.NewSystemClock()
	d := &cpaDaemon{bus: events.New(storetest.NewMemStore(), clock), api: &botAPIRecorder{reply: readSendMessageFixture(t)}}
	registry := rpc.NewRegistry()
	// The daemon's real access policy is on: an owner-cli caller (what the
	// production client sends when no principal token is set) must still reach
	// chat.append_turn and the owner-only bridge.send; no token is accepted.
	registry.SetAccessPolicy(func(string) (principal.Principal, error) {
		return principal.Principal{}, cascade.New(cascade.KindPermissionDenied, "no principal tokens in this test")
	})
	registerChatAppend(t, registry, paths.DataDir(), clock, d.bus)
	if wireBridgeSend {
		plugins.RegisterBridgeSend(registry) // before arming, as mountCascadePABridge orders it
	}
	armBridge(t, paths.DataDir(), clock, d)
	serveRegistry(t, registry, paths.SocketPath())
	return d
}

func readSendMessageFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "plugins", "cascade-pa", "telegram", "testdata", "sendmessage_ok.json"))
	if err != nil {
		t.Fatalf("read sendMessage fixture: %v", err)
	}
	return raw
}

// registerChatAppend mounts the real chat.* adapter, and seeds the bridge
// thread's internal tier through the same store.
func registerChatAppend(t *testing.T, registry *rpc.Registry, dataDir string, clock runtime.Clock, bus *events.Bus) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(dataDir, "cascade.db")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open cascade.db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	backups := filepath.Join(dataDir, "backups")
	if err := conversation.ApplyConversationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, dbPath, backups); err != nil {
		t.Fatalf("conversation schema: %v", err)
	}
	store := conversation.NewStore(db)
	if err := store.SetThreadPrivacy(ctx, cpaBridgeThread, provider.SensitivityInternal); err != nil {
		t.Fatalf("SetThreadPrivacy: %v", err)
	}
	// chat.append_turn refuses a caller-named thread that does not exist
	// (adapter_appendtarget.go), and a marker alone is not a thread. The
	// bridged chat's thread holds its inbound owner turn, as it does once a
	// real message has arrived, so the appends under test name a real thread.
	if err := store.AppendTurn(ctx, conversation.Turn{ID: "turn-bridge-seed", ThreadID: cpaBridgeThread,
		Role: conversation.RoleUser, CreatedAt: clock.Now().Unix()}); err != nil {
		t.Fatalf("seed bridge thread: %v", err)
	}
	conversation.NewAdapter(store, bus, nil, clock, "").RegisterHandlers(registry)
	if err := bridge.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, dbPath, backups); err != nil {
		t.Fatalf("bridge schema: %v", err)
	}
	if err := bridge.NewStore(db).Save(ctx, bridge.SubjectRow{Subject: telegram.SubjectFromToken(cpaBridgeToken),
		TrustTier: "paired-device", PairedAt: clock.Now(), AllowedFrom: []string{cpaBridgeChat}}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	// bridge.send resolves a thread's chat through the durable map only.
	threads, err := plugins.OpenBridgeThreadMap(ctx, db, dataDir, clock)
	if err == nil {
		_, err = threads.Bind(ctx, telegram.SubjectFromToken(cpaBridgeToken), 555000111, cpaBridgeThread)
	}
	if err != nil {
		t.Fatalf("seed map row: %v", err)
	}
}

// installProcessFirewall installs the process egress firewall the bridge's
// egress gate asks for (a process that installed none refuses to enable the
// bridge), over a file-only vault and the process detector, and detaches it
// when the test ends.
func installProcessFirewall(t *testing.T) {
	t.Helper()
	custody, err := secrets.SelectCustody(secrets.Config{
		Service: "cascade-cpa-integration-egress", Dir: t.TempDir(), ForceFileVault: true, Passphrase: "cpa-integration-pass",
		Runner: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("no keychain in tests")
		}})
	if err != nil {
		t.Fatalf("SelectCustody: %v", err)
	}
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	vault, err := secrets.NewEgressVault(broker)
	if err != nil {
		t.Fatalf("NewEgressVault: %v", err)
	}
	egress.SetProcessEngineFactory(func(r *egress.Registry) (*egress.Engine, error) {
		detector, derr := secrets.ProcessDetector()
		if derr != nil {
			return nil, derr
		}
		return egress.NewEngine(r, vault, detector)
	})
	t.Cleanup(func() { egress.SetProcessEngineFactory(nil) })
}

// armBridge assembles the real enabled bridge and arms bridge.send's sender.
func armBridge(t *testing.T, dataDir string, clock runtime.Clock, d *cpaDaemon) {
	t.Helper()
	ctx := context.Background()
	installProcessFirewall(t)
	manifest := filepath.Join(dataDir, "plugins", "cascade-pa", "manifest.toml")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o700); err != nil {
		t.Fatalf("mkdir manifest: %v", err)
	}
	if err := os.WriteFile(manifest, []byte("[modules.telegram]\nenabled = true\n"), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	vault := secrets.Config{Service: "cascade-bridge-integration", Dir: dataDir, ForceFileVault: true,
		Runner: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("no keychain in tests")
		}}
	grantToken(t, vault, dataDir)
	rt, err := plugins.NewCascadePABridge(ctx, plugins.BridgeDeps{
		DataDir: dataDir, Vault: vault, Clock: clock, Events: d.bus, Transport: d.api,
	})
	if err != nil || rt.ChatBridge == nil {
		t.Fatalf("NewCascadePABridge = %+v, %v; want an enabled bridge", rt, err)
	}
	plugins.WireChatBridge(rt, clock)
	t.Cleanup(func() {
		_ = plugins.UnwireChatBridge(context.Background())
		_ = rt.Stop(context.Background())
	})
}

func grantToken(t *testing.T, vault secrets.Config, dataDir string) {
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
	if _, err := broker.Set(context.Background(), key, []byte(cpaBridgeToken), secrets.SetUpdate); err != nil {
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

// serveRegistry serves registry on a real unix socket at sockPath.
func serveRegistry(t *testing.T, registry *rpc.Registry, sockPath string) {
	t.Helper()
	srv := daemon.NewRPCServer(registry, nil)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen %s: %v", sockPath, err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
}
