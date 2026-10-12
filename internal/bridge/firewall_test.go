// Purpose: NewFirewall's tests — the process firewall's engine over a
//
//	temp-dir file vault, the tier matrix it enforces on the bridge class, and
//	the substitution pass that makes the value source load-bearing.
//
// Constraints: NO REAL KEYCHAIN. Every secrets.Config literal here forces the
//
//	file vault AND supplies a Runner that fails, so the platform backend
//	reports unavailable and no test can write to the operator's own
//	credential store. No network, no real HOME.
//
// SPORT: internal.bridge.NewFirewall/TESTED.
package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
)

// noKeychainRunner forces the file vault: a platform helper that cannot run
// makes the platform backend report unavailable.
func noKeychainRunner(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("test: no platform keychain helper in tests")
}

func testVaultConfig(dir string) secrets.Config {
	return secrets.Config{
		Service:        "cascade-bridge-firewall-test",
		Dir:            dir,
		ForceFileVault: true,
		Runner:         noKeychainRunner,
	}
}

// installFirewall makes the process firewall one over a file vault under a
// temp dir, storing name=value when name is set, until the test ends.
func installFirewall(t *testing.T, name, value string) {
	t.Helper()
	custody, err := secrets.SelectCustody(testVaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("SelectCustody: %v", err)
	}
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	if name != "" {
		if _, err := broker.Set(context.Background(), name, []byte(value), secrets.SetUpdate); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	vault, err := secrets.NewEgressVault(broker)
	if err != nil {
		t.Fatalf("NewEgressVault: %v", err)
	}
	detector, err := secrets.ProcessDetector()
	if err != nil {
		t.Fatalf("ProcessDetector: %v", err)
	}
	egress.SetProcessEngineFactory(func(r *egress.Registry) (*egress.Engine, error) {
		return egress.NewEngine(r, vault, detector)
	})
	t.Cleanup(func() { egress.SetProcessEngineFactory(nil) })
}

// TestNewFirewall_EnforcesTheBridgeClassTierMatrix proves the engine is the
// real one: restricted and local-only are refused on the bridge class,
// internal and public are admitted.
func TestNewFirewall_EnforcesTheBridgeClassTierMatrix(t *testing.T) {
	installFirewall(t, "", "")
	engine, err := NewFirewall()
	if err != nil {
		t.Fatalf("NewFirewall: %v", err)
	}
	ctx := context.Background()
	for _, tier := range []egress.SensitivityTier{egress.TierRestricted, egress.TierLocalOnly, egress.SensitivityTier(9)} {
		if _, err := engine.InterceptClass(ctx, egress.EgressClassBridge, tier, []byte("exfiltration")); err == nil {
			t.Fatalf("tier %q was admitted onto the bridge class", tier)
		}
	}
	for _, tier := range []egress.SensitivityTier{egress.TierInternal, egress.TierPublic} {
		out, err := engine.InterceptClass(ctx, egress.EgressClassBridge, tier, []byte("ordinary content"))
		if err != nil {
			t.Fatalf("tier %q was refused: %v", tier, err)
		}
		if string(out) != "ordinary content" {
			t.Fatalf("ordinary content was rewritten to %q", out)
		}
	}
}

// TestBridgeEgressRedactsVaultValue is why the value source exists: a stored
// value with NO credential shape (at least eight bytes) is caught only by
// the exact-value pass, and leaves only as its vault reference.
func TestBridgeEgressRedactsVaultValue(t *testing.T) {
	const name, value = "BRIDGE_CANARY", "plain-canary-9f"
	installFirewall(t, name, value)
	engine, err := NewFirewall()
	if err != nil {
		t.Fatalf("NewFirewall: %v", err)
	}
	out, err := engine.InterceptClass(context.Background(), egress.EgressClassBridge,
		egress.TierInternal, []byte("here it is: "+value))
	if err != nil {
		t.Fatalf("InterceptClass: %v", err)
	}
	if strings.Contains(string(out), value) {
		t.Fatalf("a stored vault value crossed the bridge class verbatim: %q", out)
	}
	if !strings.Contains(string(out), name) {
		t.Fatalf("the stored value was not replaced by its vault reference: %q", out)
	}
}

// TestNewFirewall_RefusesWithoutAProcessFirewall: no firewall installed is
// a refusal, never an engine with only half its passes.
func TestNewFirewall_RefusesWithoutAProcessFirewall(t *testing.T) {
	egress.SetProcessEngineFactory(nil)
	engine, err := NewFirewall()
	if err != egress.ErrNoProcessFirewall { //nolint:errorlint // identity: the refusal is the sentinel itself
		t.Fatalf("err = %v, want egress.ErrNoProcessFirewall", err)
	}
	if engine != nil {
		t.Fatal("a non-nil engine came back with an error")
	}
}

// TestNewFirewall_CustodyFailureIsReturnedUnchanged: a firewall that cannot
// read the vault is an error with its own Kind and cause.
func TestNewFirewall_CustodyFailureIsReturnedUnchanged(t *testing.T) {
	cause := secrets.ErrNoCustodyAvailable()
	egress.SetProcessEngineFactory(func(*egress.Registry) (*egress.Engine, error) { return nil, cause })
	t.Cleanup(func() { egress.SetProcessEngineFactory(nil) })
	engine, err := NewFirewall()
	if err != cause || engine != nil { //nolint:errorlint // identity: returned unchanged
		t.Fatalf("NewFirewall = (%v, %v), want (nil, the custody error unchanged)", engine, err)
	}
}

// TestEncodeListRefusesAnUnencodableList covers the list-column encoder's
// failure branch: a value JSON cannot encode is an error and no column
// text, never a partial encoding written to the subject row.
func TestEncodeListRefusesAnUnencodableList(t *testing.T) {
	out, err := encodeList([]chan int{make(chan int)})
	if err == nil || out != "" {
		t.Fatalf("encodeList(unencodable) = (%q, %v), want an error and no text", out, err)
	}
	if got, err := encodeList[string](nil); err != nil || got != "[]" {
		t.Fatalf("encodeList(nil) = (%q, %v), want \"[]\"", got, err)
	}
}
