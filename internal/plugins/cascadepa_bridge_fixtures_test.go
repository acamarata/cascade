package plugins

// Purpose (this file): the bridge tests' shared fixtures — a throwaway
//   CASCADE_HOME, a path resolver that never reads the real environment, the
//   ONE custody selection every test here uses, the module manifest writer and
//   the keyed-digest helper. Split from cascadepa_bridge_wiring_test.go for
//   Art.10.3's 300-line cap.
//
// Constraints (the same three its siblings carry): NO REAL KEYCHAIN — every
//   secrets.Config literal sets ForceFileVault AND a Runner that fails, so the
//   platform backend reports unavailable and nothing reaches the operator's own
//   credential store (R-14.206); NO REAL HOME — every path comes from
//   t.TempDir(); NO NETWORK.
//
// SPORT: internal/plugins:cascadepa-bridge-fixtures/TEST (P1-E23-W5-S48-T1).

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// bridgeTestRoot is a throwaway CASCADE_HOME.
func bridgeTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o700); err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	return root
}

// bridgeTestPaths is a pathResolver over root, so no test reads the real
// environment.
func bridgeTestPaths(root string) pathResolver {
	return func() (runtime.PathProvider, error) {
		return runtime.NewPathProvider(func(key string) string {
			if key == "CASCADE_HOME" {
				return root
			}
			return ""
		}, func() (string, error) { return root, nil })
	}
}

// noKeychainRunner is the failing commandRunner that forces the file vault: a
// platform backend whose helper cannot run reports unavailable.
func noKeychainRunner(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("test: no platform keychain helper in tests")
}

// bridgeTestVaultConfig is the ONLY custody selection any test here uses.
func bridgeTestVaultConfig(dir string) secrets.Config {
	return secrets.Config{
		Service:        "cascade-bridge-wiring-test",
		Dir:            dir,
		ForceFileVault: true,
		Runner:         noKeychainRunner,
	}
}

// writeModuleManifest writes the operator-side module manifest.
func writeModuleManifest(t *testing.T, dataDir, body string) {
	t.Helper()
	path := bridgeManifestPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create manifest dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

// syntheticBotToken is the only "token" these tests handle. It is obviously not
// a real Telegram credential and is never sent anywhere.
const syntheticBotToken = "111111:SYNTHETIC-TEST-TOKEN"

// mustCodeDigest is the keyed digest a test seeds a pending code with.
func mustCodeDigest(t *testing.T, code string) string {
	t.Helper()
	key, err := cascadepa.DerivePairCodeKey(syntheticBotToken)
	if err != nil {
		t.Fatalf("DerivePairCodeKey: %v", err)
	}
	digest, err := cascadepa.CodeDigest(key, code)
	if err != nil {
		t.Fatalf("CodeDigest: %v", err)
	}
	return digest
}

// enabledBridgeDeps writes the manifest, grants the token, and returns deps
// pointing at a ready data directory plus the recording bus behind them.
func enabledBridgeDeps(t *testing.T) (BridgeDeps, *recordingBus, string) {
	t.Helper()
	dataDir := filepath.Join(bridgeTestRoot(t), "data")
	writeModuleManifest(t, dataDir, "[modules.telegram]\nenabled = true\n")
	grantAndStoreToken(t, dataDir, bridgeVaultKey, syntheticBotToken)
	bus := newRecordingBus()
	return BridgeDeps{
		DataDir: dataDir,
		Vault:   bridgeTestVaultConfig(dataDir),
		Clock:   runtime.NewSystemClock(),
		Events:  bus,
	}, bus, dataDir
}

// closeBridgeState registers state's Close (when it has one) via t.Cleanup,
// so a database handle a test opened directly through openBridgeState never
// outlives the test. Windows refuses to remove a TempDir that still holds
// cascade.db open, and t.TempDir()'s own cleanup runs with no chance to
// retry — the handle has to already be gone by then. A state built some
// other way (a test double with no Close) is left alone.
func closeBridgeState(t *testing.T, state cascadepa.BridgeState) {
	t.Helper()
	closer, ok := state.(io.Closer)
	if !ok {
		return
	}
	t.Cleanup(func() {
		if err := closer.Close(); err != nil {
			t.Errorf("close bridge state: %v", err)
		}
	})
}

// openTestConversationDB opens a fresh sqlite connection at
// dataDir/cascade.db and closes it via t.Cleanup — the test-owned stand-in
// for the connection production reuses from openBridgeState's
// bridgeStateSQLDB (ci-fix14), so a test exercising
// newBridgeThreadPrivacyResolver or NewCascadePABridgeChatHandler directly
// never leaves a Windows-unremovable handle behind.
func openTestConversationDB(t *testing.T, dataDir string) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(dataDir, "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test conversation db: %v", err)
		}
	})
	return db
}

// closeBridgeRuntime registers rt.Stop via t.Cleanup, so the state
// NewCascadePABridge opened inside enabledBridge is closed even when a test
// never calls Start (Stop on an unstarted module is a no-op per
// TestBridgeStop_DrainsAnUnstartedModule) and never calls Stop itself.
func closeBridgeRuntime(t *testing.T, rt *BridgeRuntime) {
	t.Helper()
	if rt == nil || rt.Stop == nil {
		return
	}
	t.Cleanup(func() {
		if err := rt.Stop(context.Background()); err != nil {
			t.Errorf("stop bridge runtime: %v", err)
		}
	})
}

// TestNewCascadePABridge_StopClosesTheDurableState proves the drain actually
// closes the handle openBridgeState opened, not just stops the poll: rt.Stop
// is exactly what internal/daemon/subsystem_bridge.go's drainBridge calls, so
// a second, independent Open of the SAME cascade.db succeeding right after
// Stop returns is the production path working, not a fixture's assumption
// about it.
func TestNewCascadePABridge_StopClosesTheDurableState(t *testing.T) {
	ctx := context.Background()
	deps, _, dataDir := enabledBridgeDeps(t)
	rt, err := NewCascadePABridge(ctx, deps)
	if err != nil {
		t.Fatalf("NewCascadePABridge: %v", err)
	}
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	state, err := openBridgeState(ctx, dataDir)
	if err != nil {
		t.Fatalf("openBridgeState after Stop: %v", err)
	}
	closeBridgeState(t, state)
	if err := state.Save(ctx, cascadepa.SubjectState{Subject: "tg-after-stop"}); err != nil {
		t.Fatalf("Save after Stop: %v", err)
	}
}

// TestNewCascadePABridge_FillsApprovalBridgeOnlyWhenEnabled proves the
// BridgeRuntime.ApprovalBridge field enabledBridge sets (FIX-0's producer
// leg, cascadepa_bridge_wiring.go's `ApprovalBridge: approvalLeg`) is
// actually reachable through the real constructor, not just asserted by a
// hand-built literal: a granted-token bridge must fill it, and a disabled
// bridge must leave it nil rather than a caller getting a half-armed value.
// Falsifiable: delete the `ApprovalBridge: approvalLeg` field assignment from
// enabledBridge's returned *BridgeRuntime and the first assertion below goes
// red (S-48.T4 producer confirming review, FLAG-A).
func TestNewCascadePABridge_FillsApprovalBridgeOnlyWhenEnabled(t *testing.T) {
	ctx := context.Background()
	deps, _, _ := enabledBridgeDeps(t)
	rt, err := NewCascadePABridge(ctx, deps)
	if err != nil {
		t.Fatalf("NewCascadePABridge (enabled): %v", err)
	}
	closeBridgeRuntime(t, rt)
	if rt.ApprovalBridge == nil {
		t.Fatal("a granted-token bridge left ApprovalBridge nil; WireApprovalBridge has nothing to arm")
	}

	dataDir := filepath.Join(bridgeTestRoot(t), "data")
	disabled, err := NewCascadePABridge(ctx, BridgeDeps{
		DataDir: dataDir, Vault: bridgeTestVaultConfig(dataDir),
		Clock: runtime.NewSystemClock(), Events: newRecordingBus(),
	})
	if err != nil {
		t.Fatalf("NewCascadePABridge (disabled): %v", err)
	}
	if disabled.ApprovalBridge != nil {
		t.Fatal("a disabled bridge produced a non-nil ApprovalBridge")
	}
}

// TestBridgeThreadPrivacyResolver_UsesTheGivenConnection is ci-fix14's
// sharpest falsifiable proof: the resolver must operate over the EXACT db
// passed in, never a separate one it opens for itself. Closing db out from
// under the resolver has to break it too. Mutation: reintroduce the old
// sql.Open inside newBridgeThreadPrivacyResolver (ignoring the db
// parameter) and this goes red — ThreadPrivacy would still succeed on its
// own untouched connection.
func TestBridgeThreadPrivacyResolver_UsesTheGivenConnection(t *testing.T) {
	dataDir := t.TempDir()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "cascade.db")+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}

	resolver, err := newBridgeThreadPrivacyResolver(ctx, db, dataDir, newBridgeTestClock())
	if err != nil {
		t.Fatalf("newBridgeThreadPrivacyResolver: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}

	if _, err := resolver.ThreadPrivacy(ctx, "t1"); err == nil {
		t.Fatal("ThreadPrivacy succeeded after the caller's own db was closed; " +
			"the resolver must share that exact connection, not one it opened itself")
	}
}
