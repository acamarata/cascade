// Purpose: NewFirewall's tests — a real engine over a temp-dir file vault, the
//
//	tier matrix it enforces on the bridge class, and the substitution pass
//	that makes the value source load-bearing.
//
// Constraints: NO REAL KEYCHAIN. Every secrets.Config literal here forces the
//
//	file vault AND supplies a Runner that fails, so the platform backend
//	reports unavailable and no test can write to the operator's own
//	credential store (R-14.206). No network, no real HOME.
//
// SPORT: internal.bridge.NewFirewall/TESTED (P1-E23-W5-S48-T1).
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

// TestNewFirewall_EnforcesTheBridgeClassTierMatrix proves the engine this
// package builds is the real one: restricted and local-only are refused on the
// bridge class, internal and public are admitted.
func TestNewFirewall_EnforcesTheBridgeClassTierMatrix(t *testing.T) {
	engine, err := NewFirewall(testVaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("NewFirewall: %v", err)
	}
	ctx := context.Background()
	for _, tier := range []egress.SensitivityTier{egress.TierRestricted, egress.TierLocalOnly, egress.TierUnset} {
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

// TestNewFirewall_SubstitutesAStoredValue is why the value source exists: a
// stored secret with NO credential shape is caught only by the exact-value
// pass, and that pass runs only when the engine has a vault behind it.
func TestNewFirewall_SubstitutesAStoredValue(t *testing.T) {
	dir := t.TempDir()
	const name, value = "bridge_firewall_test_entry", "correct horse battery staple 4471"
	custody, err := secrets.SelectCustody(testVaultConfig(dir))
	if err != nil {
		t.Fatalf("SelectCustody: %v", err)
	}
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	if _, err := broker.Set(context.Background(), name, []byte(value), secrets.SetUpdate); err != nil {
		t.Fatalf("Set: %v", err)
	}

	engine, err := NewFirewall(testVaultConfig(dir))
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
}

// TestNewFirewall_CustodyFailureYieldsNoEngine: a firewall that cannot read the
// vault must be an error, never a half-built engine that would filter with only
// half its passes.
func TestNewFirewall_CustodyFailureYieldsNoEngine(t *testing.T) {
	// No Service label: SelectCustody refuses before any platform backend is
	// attempted, so this cannot reach the operator's keychain.
	engine, err := NewFirewall(secrets.Config{Dir: t.TempDir()})
	if err == nil {
		t.Fatal("an engine was returned over an unselectable custody")
	}
	if engine != nil {
		t.Fatal("a non-nil engine came back with an error")
	}
}

func TestFirstErr(t *testing.T) {
	a, b := errors.New("a"), errors.New("b")
	if got := firstErr(nil, a, b); got != a {
		t.Fatalf("firstErr = %v, want the first non-nil error", got)
	}
	if got := firstErr(nil, nil); got != nil {
		t.Fatalf("firstErr = %v, want nil", got)
	}
}
