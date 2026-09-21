package plugins

// Purpose (this file): the bridge composition root's FAILURE paths — every
//   place a fault must be reported rather than absorbed. Split from its two
//   sibling test files for Art.10.3's 300-line cap and by subject: those files
//   prove the bridge works, this one proves it says so when it cannot.
//
// Each case plants a real obstacle (a file where a directory belongs, a
// directory where a database belongs, a custody selection that cannot be made)
// rather than injecting a fake error, so the assertion is about the production
// path and not about a stub.
//
// Constraints: the same three rules as its siblings — no real keychain, no real
// HOME, no network.
//
// SPORT: internal/plugins:cascadepa-bridge-failpaths/TESTED
//   (P1-E23-W5-S48-T1).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// TestReadTelegramModuleConfig_UnreadableManifestIsAnError separates "there is
// no manifest" (the default-off answer) from "there is one and this process
// cannot read it" (a fault the operator has to hear about).
func TestReadTelegramModuleConfig_UnreadableManifestIsAnError(t *testing.T) {
	dir := bridgeTestRoot(t)
	// A DIRECTORY where the manifest should be: present, unreadable as a file.
	if err := os.MkdirAll(bridgeManifestPath(dir), 0o700); err != nil {
		t.Fatalf("create the blocking directory: %v", err)
	}
	if _, err := readTelegramModuleConfig(dir); err == nil {
		t.Fatal("an unreadable manifest was reported as default-off")
	}
}

// TestNewBridgeEgressGateFor_FirewallFailurePropagates: a firewall that cannot
// be built must not yield a gate, because a nil-engine gate that refused
// everything would look identical to a firewall doing its job.
func TestNewBridgeEgressGateFor_FirewallFailurePropagates(t *testing.T) {
	// No Service label: SelectCustody refuses outright and never reaches a
	// platform backend, so this cannot touch the operator's keychain.
	if _, err := newBridgeEgressGateFor(secrets.Config{Dir: bridgeTestRoot(t)}); err == nil {
		t.Fatal("a gate was built over an unbuildable firewall")
	}
}

func TestResolveBridgeToken_CustodyFailurePropagates(t *testing.T) {
	if _, err := resolveBridgeToken(context.Background(),
		secrets.Config{Dir: bridgeTestRoot(t)}, bridgeVaultKey); err == nil {
		t.Fatal("a token was resolved over an unselectable custody")
	}
}

// TestBridgeDeviceRegistrar_StoreFailureIsReported is the other half of the
// bind rule: a registrar whose backing store cannot be read or written reports
// it, so BindingStore.Bind refuses instead of recording a pairing the device
// registry never heard of.
func TestBridgeDeviceRegistrar_StoreFailureIsReported(t *testing.T) {
	dir := bridgeTestRoot(t)
	// A regular file where the registrar's own directory belongs: every path
	// under it is ENOTDIR.
	if err := os.WriteFile(filepath.Join(dir, "bridge"), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("plant the blocking file: %v", err)
	}
	_, err := newBridgeDeviceRegistrar(dir).RegisterPairedDevice(
		context.Background(), "tg-abc", "111", time.Now())
	if err == nil {
		t.Fatal("RegisterPairedDevice reported success against an unusable store")
	}
	if strings.Contains(err.Error(), "already enrolled") {
		t.Fatalf("a store failure was reported as a conflict: %v", err)
	}
}

// TestOpenBridgeState_UncreatableDataDirectoryIsReported: a data directory this
// process cannot create is a fault, not an empty store.
func TestOpenBridgeState_UncreatableDataDirectoryIsReported(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("plant the blocking file: %v", err)
	}
	if _, err := openBridgeState(context.Background(), filepath.Join(blocker, "data")); err == nil {
		t.Fatal("a bridge state store was opened under an uncreatable directory")
	}
}

// TestOpenBridgeState_MigrationFailureIsReported: the schema is applied before
// the store is handed back, so a database that cannot be migrated is refused
// rather than returned half-usable.
func TestOpenBridgeState_MigrationFailureIsReported(t *testing.T) {
	dir := filepath.Join(bridgeTestRoot(t), "data")
	// A DIRECTORY named cascade.db: sql.Open succeeds (it is lazy), the first
	// real statement does not.
	if err := os.MkdirAll(filepath.Join(dir, "cascade.db"), 0o700); err != nil {
		t.Fatalf("plant the blocking directory: %v", err)
	}
	if _, err := openBridgeState(context.Background(), dir); err == nil {
		t.Fatal("a store was returned over a database that could not be migrated")
	}
}

// TestNewCascadePABridge_StoreFailurePropagates: an enabled, granted bridge
// whose database cannot be opened is a real FAULT — reported to the daemon as a
// failed subsystem, never a bridge that looks configured and stores nothing.
func TestNewCascadePABridge_StoreFailurePropagates(t *testing.T) {
	dataDir := filepath.Join(bridgeTestRoot(t), "data")
	writeModuleManifest(t, dataDir, "[modules.telegram]\nenabled = true\n")
	grantAndStoreToken(t, dataDir, bridgeVaultKey, syntheticBotToken)
	// A DIRECTORY named cascade.db: the migration cannot run over it.
	if err := os.MkdirAll(filepath.Join(dataDir, "cascade.db"), 0o700); err != nil {
		t.Fatalf("plant the blocking directory: %v", err)
	}
	if _, err := NewCascadePABridge(context.Background(), BridgeDeps{
		DataDir: dataDir, Vault: bridgeTestVaultConfig(dataDir),
		Clock: runtime.NewSystemClock(), Events: newRecordingBus(),
	}); err == nil {
		t.Fatal("a bridge was assembled over a database that could not be migrated")
	}
}

// TestBridgeStateAdapter_TranslatesACompareAndSwapConflict is the one place
// internal/bridge's vocabulary meets the plugin's: a stale write has to arrive
// as cascadepa.ErrStateConflict, because that is the ONLY error the plugin's
// retry re-reads on. Losing the translation turns a refused write into a lost
// update, which is the defect the compare-and-swap exists to prevent.
func TestBridgeStateAdapter_TranslatesACompareAndSwapConflict(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(bridgeTestRoot(t), "data")
	state, err := openBridgeState(ctx, dir)
	if err != nil {
		t.Fatalf("openBridgeState: %v", err)
	}
	if err := state.Save(ctx, cascadepa.SubjectState{Subject: "tg-cas", AllowedFrom: []string{"111"}}); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	first, _, err := state.Load(ctx, "tg-cas")
	if err != nil {
		t.Fatalf("Load(first): %v", err)
	}
	stale, _, err := state.Load(ctx, "tg-cas")
	if err != nil {
		t.Fatalf("Load(stale): %v", err)
	}
	first.Offset = 5
	if err := state.Save(ctx, first); err != nil {
		t.Fatalf("Save(first): %v", err)
	}
	stale.AllowedFrom = nil
	err = state.Save(ctx, stale)
	if !cascadepa.IsStateConflict(err) {
		t.Fatalf("a stale Save through the adapter gave %v, want the plugin-facing conflict", err)
	}
	got, _, _ := state.Load(ctx, "tg-cas")
	if got.Offset != 5 || len(got.AllowedFrom) != 1 {
		t.Fatalf("the refused write still landed: %+v", got)
	}
}

// TestPairClient_BuildsARealTransportWhenNoDoerIsInjected covers the production
// branch of rpcClient: with no injected doer it resolves the socket path and
// dials it. There is no daemon on this temp HOME, so the dial fails — which is
// the point: the error names the verb and the socket, and no code is printed.
func TestPairClient_BuildsARealTransportWhenNoDoerIsInjected(t *testing.T) {
	root := bridgeTestRoot(t)
	pair := newCascadePAPairClient(client.UnixDialer, time.Second, bridgeTestPaths(root))
	_, err := pair.IssueCode(context.Background(), "tg-abc")
	if err == nil {
		t.Fatal("a code was issued with no daemon listening")
	}
	if !strings.Contains(err.Error(), "pa.pair_code") {
		t.Fatalf("error = %v, want it to name the verb it could not reach", err)
	}
}
