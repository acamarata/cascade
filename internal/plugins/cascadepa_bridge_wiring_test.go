package plugins

// Purpose (this file): the bridge composition root's tests — the module gate,
//   the vault read, what the daemon is handed, the routes the wiring mounts, the
//   journal it wires, and `cascade pa pair`'s own RPC client.
//
// Constraints, all of them hard rules rather than preferences:
//   - NO REAL KEYCHAIN. Every secrets.Config literal here sets ForceFileVault
//     AND a Runner that fails, so the platform backend reports unavailable and
//     nothing can reach the operator's own credential store (R-14.206).
//   - NO REAL HOME. Every path comes from t.TempDir() through an injected
//     getenv (Art.7.1); runtime.NewDefaultPathProvider is never called.
//   - NO NETWORK. Nothing here calls Start, so the real transport never dials;
//     the pair client is driven through a fake round trip.
//
// SPORT: internal/plugins:cascadepa-bridge-wiring/TESTED (P1-E23-W5-S48-T1).

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

func TestReadTelegramModuleConfig_AbsentManifestIsDefaultOff(t *testing.T) {
	cfg, err := readTelegramModuleConfig(bridgeTestRoot(t))
	if err != nil {
		t.Fatalf("readTelegramModuleConfig: %v", err)
	}
	if cfg.Enabled {
		t.Fatal("the telegram module is enabled with no manifest at all")
	}
	if cfg.VaultKey != bridgeVaultKey {
		t.Fatalf("VaultKey = %q, want %q", cfg.VaultKey, bridgeVaultKey)
	}
}

func TestReadTelegramModuleConfig_EnabledAndKeyOverride(t *testing.T) {
	dir := bridgeTestRoot(t)
	writeModuleManifest(t, dir, "[modules.telegram]\nenabled = true\n"+
		"bot_token_vault_key = \"cascade-pa.telegram.other_token\"\n")
	cfg, err := readTelegramModuleConfig(dir)
	if err != nil {
		t.Fatalf("readTelegramModuleConfig: %v", err)
	}
	if !cfg.Enabled {
		t.Fatal("enabled = true was not read")
	}
	if cfg.VaultKey != "cascade-pa.telegram.other_token" {
		t.Fatalf("VaultKey = %q", cfg.VaultKey)
	}
}

func TestReadTelegramModuleConfig_EmptyKeyFallsBackToTheConstant(t *testing.T) {
	dir := bridgeTestRoot(t)
	writeModuleManifest(t, dir, "[modules.telegram]\nenabled = true\n")
	cfg, err := readTelegramModuleConfig(dir)
	if err != nil {
		t.Fatalf("readTelegramModuleConfig: %v", err)
	}
	if cfg.VaultKey != bridgeVaultKey {
		t.Fatalf("VaultKey = %q, want the default %q", cfg.VaultKey, bridgeVaultKey)
	}
}

func TestReadTelegramModuleConfig_MalformedTOMLIsAnError(t *testing.T) {
	dir := bridgeTestRoot(t)
	writeModuleManifest(t, dir, "[modules.telegram\nenabled = yes")
	if _, err := readTelegramModuleConfig(dir); err == nil {
		t.Fatal("a malformed manifest was accepted")
	}
}

func TestNewCascadePABridge_RefusesIncompleteDeps(t *testing.T) {
	dataDir := filepath.Join(bridgeTestRoot(t), "data")
	full := BridgeDeps{DataDir: dataDir, Vault: bridgeTestVaultConfig(dataDir),
		Clock: runtime.NewSystemClock(), Events: newRecordingBus()}
	for name, mutate := range map[string]func(*BridgeDeps){
		"no data dir": func(d *BridgeDeps) { d.DataDir = "" },
		"no clock":    func(d *BridgeDeps) { d.Clock = nil },
		"no journal":  func(d *BridgeDeps) { d.Events = nil },
		"no custody":  func(d *BridgeDeps) { d.Vault = secrets.Config{} },
	} {
		deps := full
		mutate(&deps)
		if _, err := NewCascadePABridge(context.Background(), deps); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

// TestNewCascadePABridge_DisabledByDefault: with no manifest there is no poll,
// the daemon is told why, and pa.pair_code still ANSWERS — with a refusal that
// names what is missing, because a verb that vanishes cannot be diagnosed.
func TestNewCascadePABridge_DisabledByDefault(t *testing.T) {
	dataDir := filepath.Join(bridgeTestRoot(t), "data")
	rt, err := NewCascadePABridge(context.Background(), BridgeDeps{
		DataDir: dataDir, Vault: bridgeTestVaultConfig(dataDir),
		Clock: runtime.NewSystemClock(), Events: newRecordingBus(),
	})
	if err != nil {
		t.Fatalf("NewCascadePABridge: %v", err)
	}
	if rt.Start != nil {
		t.Fatal("a poll was started with no module manifest at all")
	}
	if !strings.Contains(rt.DisabledReason, "disabled") {
		t.Fatalf("DisabledReason = %q, want the disabled-module reason", rt.DisabledReason)
	}
	if rt.IssueCode == nil {
		t.Fatal("pa.pair_code has no implementation on a disabled bridge")
	}
	if _, err := rt.IssueCode(context.Background(), ""); err == nil {
		t.Fatal("a code was issued by a disabled bridge")
	} else if !strings.Contains(err.Error(), "no bridge module is enabled") {
		t.Fatalf("refusal = %v, want the no-module refusal", err)
	}
}

// TestNewCascadePABridge_EnabledWithoutAGrantedToken: the manifest alone is not
// enough. The second gate is a granted vault entry, and its absence is reported
// rather than guessed around.
func TestNewCascadePABridge_EnabledWithoutAGrantedToken(t *testing.T) {
	dataDir := filepath.Join(bridgeTestRoot(t), "data")
	writeModuleManifest(t, dataDir, "[modules.telegram]\nenabled = true\n")
	rt, err := NewCascadePABridge(context.Background(), BridgeDeps{
		DataDir: dataDir, Vault: bridgeTestVaultConfig(dataDir),
		Clock: runtime.NewSystemClock(), Events: newRecordingBus(),
	})
	if err != nil {
		t.Fatalf("NewCascadePABridge: %v", err)
	}
	if rt.Start != nil {
		t.Fatal("the poll started with no bot token in the vault")
	}
	if rt.DisabledReason == "" {
		t.Fatal("no reason was recorded for a bridge that could not read its token")
	}
	if strings.Contains(rt.DisabledReason, syntheticBotToken) {
		t.Fatalf("the reason leaks the credential: %q", rt.DisabledReason)
	}
}

// TestNewCascadePABridge_BothGatesPass is the success path: what the daemon gets
// handed, and the two routes and the journal sink the wiring mounts.
func TestNewCascadePABridge_BothGatesPass(t *testing.T) {
	deps, _, _ := enabledBridgeDeps(t)
	rt, err := NewCascadePABridge(context.Background(), deps)
	if err != nil {
		t.Fatalf("NewCascadePABridge: %v", err)
	}
	if rt.Start == nil || rt.Stop == nil || rt.IssueCode == nil {
		t.Fatalf("the daemon was handed an incomplete subsystem: %+v", rt)
	}
	if !strings.HasPrefix(rt.Subject, "tg-") || strings.Contains(rt.Subject, "SYNTHETIC") {
		t.Fatalf("subject = %q, want a token digest that does not carry the token", rt.Subject)
	}
	if len(rt.Routes) != 2 ||
		rt.Routes[0] != telegram.HandlerText || rt.Routes[1] != telegram.HandlerCallbackQuery {
		t.Fatalf("Routes = %v, want both transports routed; an admitted message with no route is "+
			"dropped inside the module with no trace", rt.Routes)
	}
	// AC#16: production must hand the module a RECORDING sink, never the
	// plugin's discarding default. This is the value passed to NewModule, one
	// line above where it is stored.
	if rt.sink == nil {
		t.Fatal("the module was wired with no lockout sink; a lockout would be discarded")
	}
	if _, isJournal := rt.sink.(*bridgeJournal); !isJournal {
		t.Fatalf("the lockout sink is %T, want the bus-backed journal", rt.sink)
	}
}

// TestBridgeIssuance_IsRedeemableThroughTheSameDurableRow is the property the
// daemon home exists for: a code minted by pa.pair_code verifies against the
// SAME row, under a key derived from the SAME credential.
func TestBridgeIssuance_IsRedeemableThroughTheSameDurableRow(t *testing.T) {
	ctx := context.Background()
	deps, _, dataDir := enabledBridgeDeps(t)
	rt, err := NewCascadePABridge(ctx, deps)
	if err != nil {
		t.Fatalf("NewCascadePABridge: %v", err)
	}
	res, err := rt.IssueCode(ctx, "")
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	if len(res.Code) != cascadepa.PairCodeLength || res.Subject != rt.Subject || res.ExpiresAt.IsZero() {
		t.Fatalf("issued %+v", res)
	}

	// A second, independent verifier over the same database and the same
	// derived key — which is what the poll goroutine holds.
	state, err := openBridgeState(ctx, dataDir)
	if err != nil {
		t.Fatalf("openBridgeState: %v", err)
	}
	key, err := cascadepa.DerivePairCodeKey(syntheticBotToken)
	if err != nil {
		t.Fatalf("DerivePairCodeKey: %v", err)
	}
	verifier := cascadepa.NewPairCodeStore(runtime.NewSystemClock(), state, key)
	out, err := verifier.VerifyAndConsume(ctx, rt.Subject, res.Code)
	if err != nil {
		t.Fatalf("VerifyAndConsume: %v", err)
	}
	if !out.Bound {
		t.Fatal("a code issued through pa.pair_code was not redeemable against its own durable row")
	}
	// And a store with a key derived from ANOTHER credential cannot verify it,
	// which is what makes a stolen database useless on its own.
	otherKey, err := cascadepa.DerivePairCodeKey(syntheticBotToken + "-other")
	if err != nil {
		t.Fatalf("DerivePairCodeKey(other): %v", err)
	}
	again, err := rt.IssueCode(ctx, "")
	if err != nil {
		t.Fatalf("second IssueCode: %v", err)
	}
	stranger := cascadepa.NewPairCodeStore(runtime.NewSystemClock(), state, otherKey)
	wrong, err := stranger.VerifyAndConsume(ctx, rt.Subject, again.Code)
	if err != nil {
		t.Fatalf("VerifyAndConsume(other key): %v", err)
	}
	if wrong.Bound {
		t.Fatal("a code verified under a key derived from a different credential")
	}
}

// TestBridgeIssuance_RefusesAForeignSubject: issuing under a subject no module
// verifies looks exactly like a working command and fails ten minutes later, in
// Telegram, with no explanation.
func TestBridgeIssuance_RefusesAForeignSubject(t *testing.T) {
	ctx := context.Background()
	deps, _, _ := enabledBridgeDeps(t)
	rt, err := NewCascadePABridge(ctx, deps)
	if err != nil {
		t.Fatalf("NewCascadePABridge: %v", err)
	}
	_, err = rt.IssueCode(ctx, "tg-somebody-elses-bot")
	if err == nil {
		t.Fatal("a code was issued for a subject this daemon runs no bridge for")
	}
	if !strings.Contains(err.Error(), "runs no bridge for subject") {
		t.Fatalf("refusal = %v", err)
	}
}

// TestBridgeStop_DrainsAnUnstartedModule: Stop on a module that never started is
// a no-op rather than an error, so the daemon's drain is unconditional.
func TestBridgeStop_DrainsAnUnstartedModule(t *testing.T) {
	deps, _, _ := enabledBridgeDeps(t)
	rt, err := NewCascadePABridge(context.Background(), deps)
	if err != nil {
		t.Fatalf("NewCascadePABridge: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("Stop on an unstarted module: %v", err)
	}
}

// TestBridgeVaultConfig_IsTheVaultsOwnLabel: the bridge must read the token from
// the SAME place `cascade vault set` wrote it. A second service label would make
// an operator's stored secret invisible to the bridge with no error anywhere.
func TestBridgeVaultConfig_IsTheVaultsOwnLabel(t *testing.T) {
	cfg := BridgeVaultConfig("/tmp/example-data-dir")
	if cfg.Service != secrets.DefaultVaultService {
		t.Fatalf("Service = %q, want %q", cfg.Service, secrets.DefaultVaultService)
	}
	if cfg.Dir != "/tmp/example-data-dir" {
		t.Fatalf("Dir = %q", cfg.Dir)
	}
	if cfg.ForceFileVault {
		t.Fatal("production custody selection forces the file vault")
	}
}
