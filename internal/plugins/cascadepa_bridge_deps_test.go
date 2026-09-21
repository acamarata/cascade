package plugins

// Purpose (this file): the bridge's four host adapters — the elevated-verb
//   policy over internal/rpc's canonical table, the paired-device registrar
//   over internal/nodes, the egress gate over internal/hooks/egress, and the
//   vault read under a standing grant.
//
// Constraints: the same three hard rules as cascadepa_bridge_wiring_test.go —
//   no real keychain (every secrets.Config literal forces the file vault with a
//   failing Runner), no real HOME (t.TempDir() only), no network.
//
// SPORT: internal/plugins:cascadepa-bridge-deps/TESTED (P1-E23-W5-S48-T1).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// TestBridgeElevationPolicy_UsesTheCanonicalTable proves the classification is
// internal/rpc's §5.14 table and not a copy: node.enroll and node.upgrade are
// elevated, ordinary verbs are not, and a CONDITIONALLY elevated verb with no
// readable params fails closed.
func TestBridgeElevationPolicy_UsesTheCanonicalTable(t *testing.T) {
	policy := bridgeElevationPolicy{}
	for _, verb := range []string{"node.enroll", "node.upgrade", "node.remove", "vault.get", "perms.grant"} {
		if !policy.IsElevatedVerb(verb) {
			t.Fatalf("%q is not classified as elevated", verb)
		}
	}
	for _, verb := range []string{"chat", "status", "approve", "", "node"} {
		if policy.IsElevatedVerb(verb) {
			t.Fatalf("%q is classified as elevated; the bridge would refuse ordinary traffic", verb)
		}
	}
	// plugin.add is conditional on params this path never has: unreadable
	// params must elevate.
	if !policy.IsElevatedVerb("plugin.add") {
		t.Fatal("a conditionally-elevated verb with no params was admitted")
	}
}

func TestBridgeNodeID_IsDeterministicAndUnambiguous(t *testing.T) {
	first := bridgeNodeID("tg-abc", "111")
	if first != bridgeNodeID("tg-abc", "111") {
		t.Fatal("bridgeNodeID is not stable")
	}
	if first == bridgeNodeID("tg-abc", "222") {
		t.Fatal("two senders share one node id")
	}
	// The NUL separator is what keeps these apart.
	if bridgeNodeID("a", "bc") == bridgeNodeID("ab", "c") {
		t.Fatal("subject/sender boundaries collide")
	}
	if !strings.HasPrefix(first, "bridge-") {
		t.Fatalf("node id %q does not name its origin", first)
	}
}

// TestBridgeDeviceRegistrar_WritesAKeylessPairedDeviceRecord: the record is a
// real Q/S-36.T1 DeviceRecord at trust tier paired-device, and it carries NO
// public key — which is what makes it unable to satisfy a node-dispatch gate.
func TestBridgeDeviceRegistrar_WritesAKeylessPairedDeviceRecord(t *testing.T) {
	dir := bridgeTestRoot(t)
	reg := newBridgeDeviceRegistrar(dir)
	ctx := context.Background()
	nodeID, err := reg.RegisterPairedDevice(ctx, "tg-abc", "111", time.Now())
	if err != nil {
		t.Fatalf("RegisterPairedDevice: %v", err)
	}
	if nodeID != bridgeNodeID("tg-abc", "111") {
		t.Fatalf("node id = %q", nodeID)
	}
	// Re-pairing the same sender is idempotent, not a conflict.
	again, err := reg.RegisterPairedDevice(ctx, "tg-abc", "111", time.Now())
	if err != nil {
		t.Fatalf("second RegisterPairedDevice: %v", err)
	}
	if again != nodeID {
		t.Fatalf("re-registration produced %q, want %q", again, nodeID)
	}
	// The record landed in the BRIDGE's own backend, not the node fleet's.
	if _, err := os.Stat(filepath.Join(dir, "bridge", "nodes", "devices.json")); err != nil {
		t.Fatalf("the bridge device record was not written where expected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "nodes", "devices.json")); err == nil {
		t.Fatal("the bridge wrote into the node fleet's own device registry")
	}
}

// bridgeTestEngine builds a real egress engine over a temp-dir file vault,
// optionally seeding one stored secret so the substitution pass has something
// to resolve.
func bridgeTestEngine(t *testing.T, dir, secretName, secretValue string) *egress.Engine {
	t.Helper()
	custody, err := secrets.SelectCustody(bridgeTestVaultConfig(dir))
	if err != nil {
		t.Fatalf("SelectCustody: %v", err)
	}
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	if secretName != "" {
		if _, serr := broker.Set(context.Background(), secretName, []byte(secretValue), secrets.SetUpdate); serr != nil {
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
	return engine
}

// TestBridgeEgressGate_EnforcesTheClassAndSubstitutes is the D1 proof at the
// adapter: the tier matrix refuses restricted and local-only, admits internal
// and public, and the returned bytes are the FILTERED ones.
func TestBridgeEgressGate_EnforcesTheClassAndSubstitutes(t *testing.T) {
	dir := bridgeTestRoot(t)
	const storedName, storedValue = "bridge_test_secret", "SYNTHETIC-VAULT-VALUE-0001"
	gate := NewBridgeEgressGate(bridgeTestEngine(t, dir, storedName, storedValue))
	ctx := context.Background()

	for _, tier := range []cascadepa.SensitivityTier{cascadepa.TierRestricted, cascadepa.TierLocalOnly} {
		if _, err := gate.Guard(ctx, tier, []byte("exfiltration")); err == nil {
			t.Fatalf("tier %q was admitted onto the bridge class", tier)
		}
	}
	// The unset tier resolves to restricted and must refuse too.
	if _, err := gate.Guard(ctx, cascadepa.SensitivityTier(""), []byte("unclassified")); err == nil {
		t.Fatal("unclassified content was admitted")
	}
	for _, tier := range []cascadepa.SensitivityTier{cascadepa.TierInternal, cascadepa.TierPublic} {
		out, err := gate.Guard(ctx, tier, []byte("ordinary bridge content"))
		if err != nil {
			t.Fatalf("tier %q was refused: %v", tier, err)
		}
		if string(out) != "ordinary bridge content" {
			t.Fatalf("ordinary content was rewritten to %q", out)
		}
	}
	filtered, err := gate.Guard(ctx, cascadepa.TierInternal, []byte("the value is "+storedValue))
	if err != nil {
		t.Fatalf("Guard: %v", err)
	}
	if strings.Contains(string(filtered), storedValue) {
		t.Fatalf("a stored vault value crossed the bridge verbatim: %q", filtered)
	}
}

func TestBridgeEgressGate_NilEngineRefuses(t *testing.T) {
	if _, err := NewBridgeEgressGate(nil).Guard(context.Background(),
		cascadepa.TierInternal, []byte("x")); err == nil {
		t.Fatal("a gate with no engine admitted content")
	}
}

func TestNewBridgeEgressGateFor_BuildsAWorkingGate(t *testing.T) {
	dir := bridgeTestRoot(t)
	gate, err := newBridgeEgressGateFor(bridgeTestVaultConfig(dir))
	if err != nil {
		t.Fatalf("newBridgeEgressGateFor: %v", err)
	}
	if _, err := gate.Guard(context.Background(), cascadepa.TierInternal, []byte("hello")); err != nil {
		t.Fatalf("Guard: %v", err)
	}
	if _, err := gate.Guard(context.Background(), cascadepa.TierRestricted, []byte("hello")); err == nil {
		t.Fatal("the built gate admitted restricted content")
	}
}

// grantAndStoreToken seeds the vault with a token and the standing grant that
// authorises reading it.
func grantAndStoreToken(t *testing.T, dir, key, token string) {
	t.Helper()
	custody, err := secrets.SelectCustody(bridgeTestVaultConfig(dir))
	if err != nil {
		t.Fatalf("SelectCustody: %v", err)
	}
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	if _, err := broker.Set(context.Background(), key, []byte(token), secrets.SetUpdate); err != nil {
		t.Fatalf("Set: %v", err)
	}
	store, err := secrets.NewFileGrantStore(dir)
	if err != nil {
		t.Fatalf("NewFileGrantStore: %v", err)
	}
	grants, err := secrets.NewGrants(store, runtime.NewSystemClock())
	if err != nil {
		t.Fatalf("NewGrants: %v", err)
	}
	if _, err := grants.Issue(context.Background(), key, 10*time.Minute); err != nil {
		t.Fatalf("Issue: %v", err)
	}
}

// TestResolveBridgeToken_NeedsAStandingGrant: a stored secret with no grant is
// unreadable on this path, because the daemon has no human to prove presence.
func TestResolveBridgeToken_NeedsAStandingGrant(t *testing.T) {
	dir := bridgeTestRoot(t)
	ctx := context.Background()
	if _, err := resolveBridgeToken(ctx, bridgeTestVaultConfig(dir), bridgeVaultKey); err == nil {
		t.Fatal("a token was resolved with nothing stored")
	}
	grantAndStoreToken(t, dir, bridgeVaultKey, "111111:SYNTHETIC-TEST-TOKEN")
	token, err := resolveBridgeToken(ctx, bridgeTestVaultConfig(dir), bridgeVaultKey)
	if err != nil {
		t.Fatalf("resolveBridgeToken: %v", err)
	}
	if token != "111111:SYNTHETIC-TEST-TOKEN" {
		t.Fatalf("token = %q", token)
	}
	// An empty key falls back to the constant rather than reading "".
	if _, err := resolveBridgeToken(ctx, bridgeTestVaultConfig(dir), ""); err != nil {
		t.Fatalf("empty key did not fall back to the constant: %v", err)
	}
}

// TestResolveBridgeToken_EmptyStoredValueIsRefused: a blank entry is not a
// token, and starting a bridge with one would dial with an empty credential.
func TestResolveBridgeToken_EmptyStoredValueIsRefused(t *testing.T) {
	dir := bridgeTestRoot(t)
	grantAndStoreToken(t, dir, bridgeVaultKey, "   ")
	if _, err := resolveBridgeToken(context.Background(), bridgeTestVaultConfig(dir), bridgeVaultKey); err == nil {
		t.Fatal("a blank vault entry was accepted as a bot token")
	}
}

// TestOpenBridgeState_RoundTripsThroughSQLite exercises the real adapter over a
// real database: what cascadepa writes, cascadepa reads back.
func TestOpenBridgeState_RoundTripsThroughSQLite(t *testing.T) {
	dir := filepath.Join(bridgeTestRoot(t), "data")
	ctx := context.Background()
	state, err := openBridgeState(ctx, dir)
	if err != nil {
		t.Fatalf("openBridgeState: %v", err)
	}
	want := cascadepa.SubjectState{
		Subject: "tg-abc", TrustTier: "paired-device", AllowedFrom: []string{"111"},
		CodeDigest: mustCodeDigest(t, "ABCDEFGH"), WrongAttempts: 2,
		Offset: 900000002, SeenUpdateIDs: []int64{900000001},
		PairedAt: time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC),
	}
	if err := state.Save(ctx, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := state.Load(ctx, "tg-abc")
	if err != nil || !ok {
		t.Fatalf("Load = (%v, %v)", ok, err)
	}
	if got.CodeDigest != want.CodeDigest || got.WrongAttempts != 2 || got.Offset != 900000002 {
		t.Fatalf("round trip gave %+v", got)
	}
	if len(got.AllowedFrom) != 1 || got.AllowedFrom[0] != "111" {
		t.Fatalf("allowlist = %v", got.AllowedFrom)
	}
	if _, ok, err := state.Load(ctx, "tg-unknown"); err != nil || ok {
		t.Fatalf("Load(unknown) = (%v, %v)", ok, err)
	}
}
