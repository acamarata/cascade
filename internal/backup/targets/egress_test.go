// Purpose: proves the backup-target egress wiring: the class is
// registered on the DEFAULT registry with the right policy (R-21.265),
// and acquireBackupTargetCapability/interceptOutbound actually transit a
// real *egress.Engine (secrets.NewBroker + secrets.NewEgressVault +
// secrets.NewDetector, the same construction every other landed egress
// consumer's tests use — internal/providers/intake/core_test.go's
// testDeps precedent).
//
// SPORT: internal.backup.targets.egress/ADDED (P1-E19-W4-S41-T3).
package targets_test

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/backup/targets"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeCustody is an in-memory secrets.Custody, used only to build a real
// secrets.Broker/EgressVault for these tests — no real keychain touched.
type fakeCustody struct{ values map[string][]byte }

func newFakeCustody() *fakeCustody { return &fakeCustody{values: map[string][]byte{}} }

func (f *fakeCustody) Name() string    { return "fake" }
func (f *fakeCustody) Available() bool { return true }
func (f *fakeCustody) Set(_ context.Context, name string, value []byte) error {
	f.values[name] = value
	return nil
}
func (f *fakeCustody) Get(_ context.Context, name string) ([]byte, error) {
	v, ok := f.values[name]
	if !ok {
		return nil, secrets.ErrSecretNotFound(name)
	}
	return v, nil
}
func (f *fakeCustody) Delete(_ context.Context, name string) error {
	if _, ok := f.values[name]; !ok {
		return secrets.ErrSecretNotFound(name)
	}
	delete(f.values, name)
	return nil
}
func (f *fakeCustody) List(context.Context) ([]string, error) {
	var out []string
	for k := range f.values {
		out = append(out, k)
	}
	return out, nil
}

// allowGate authorizes every elevated verb; these tests never exercise
// the elevation gate itself.
type allowGate struct{}

func (allowGate) Authorize(context.Context, string) error { return nil }

// testEgressEngine builds a real *egress.Engine over the DEFAULT
// registry (the one classes.go registers EgressClassBackupTarget on at
// package init) and an in-memory custody backend.
func testEgressEngine(t *testing.T) *egress.Engine {
	t.Helper()
	broker, err := secrets.NewBroker(newFakeCustody(), allowGate{})
	if err != nil {
		t.Fatalf("secrets.NewBroker: %v", err)
	}
	vault, err := secrets.NewEgressVault(broker)
	if err != nil {
		t.Fatalf("secrets.NewEgressVault: %v", err)
	}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("secrets.NewDetector: %v", err)
	}
	engine, err := egress.NewEngine(egress.DefaultRegistry(), vault, detector)
	if err != nil {
		t.Fatalf("egress.NewEngine: %v", err)
	}
	return engine
}

// TestEgressClassBackupTarget_InterceptConfigRegistered is this ticket's
// named acceptance test: EgressClassBackupTarget is registered on the
// default registry with Enabled+AllowRestricted, and a capability
// acquired for it actually passes real content through Intercept.
func TestEgressClassBackupTarget_InterceptConfigRegistered(t *testing.T) {
	cfg, ok := egress.DefaultRegistry().Lookup(egress.EgressClassBackupTarget)
	if !ok {
		t.Fatal("EgressClassBackupTarget is not registered on the default registry")
	}
	if !cfg.Enabled {
		t.Fatal("EgressClassBackupTarget.Enabled = false, want true")
	}
	if !cfg.AllowRestricted {
		t.Fatal("EgressClassBackupTarget.AllowRestricted = false, want true (a backup of restricted material is the point)")
	}

	engine := testEgressEngine(t)
	token, err := engine.Capability(egress.EgressClassBackupTarget)
	if err != nil {
		t.Fatalf("engine.Capability(EgressClassBackupTarget): %v", err)
	}
	out, err := engine.Intercept(context.Background(), token, egress.TierRestricted, []byte("ciphertext-shaped-payload"))
	if err != nil {
		t.Fatalf("Intercept: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("Intercept returned empty output for non-empty input")
	}
}

// TestEgressClassBackupTarget_ZeroCapabilityRefused proves a zero
// Capability (never obtained from a registry) is refused before any
// content is read, matching Intercept's documented admission order.
func TestEgressClassBackupTarget_ZeroCapabilityRefused(t *testing.T) {
	engine := testEgressEngine(t)
	_, err := engine.Intercept(context.Background(), egress.Capability{}, egress.TierRestricted, []byte("x"))
	if err == nil {
		t.Fatal("Intercept(zero capability) returned nil error")
	}
}

// TestAcquireBackupTargetCapability_DisabledClassRefused proves the
// acquisition helper's error path (a registered-but-disabled class)
// surfaces as a typed refusal, not just the always-enabled default
// registry's happy path.
func TestAcquireBackupTargetCapability_DisabledClassRefused(t *testing.T) {
	reg := egress.NewRegistry()
	reg.MustRegister(egress.EgressClassBackupTarget, egress.InterceptConfig{Enabled: false, Owner: "test"})
	broker, err := secrets.NewBroker(newFakeCustody(), allowGate{})
	if err != nil {
		t.Fatalf("secrets.NewBroker: %v", err)
	}
	vault, err := secrets.NewEgressVault(broker)
	if err != nil {
		t.Fatalf("secrets.NewEgressVault: %v", err)
	}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("secrets.NewDetector: %v", err)
	}
	engine, err := egress.NewEngine(reg, vault, detector)
	if err != nil {
		t.Fatalf("egress.NewEngine: %v", err)
	}
	_, rerr := targets.NewRcloneTarget("remote:bucket", &fakeRunner{}, engine)
	if !cascade.HasKind(rerr, cascade.KindPolicyDenied) {
		t.Fatalf("NewRcloneTarget(disabled class) = %v, want KindPolicyDenied", rerr)
	}
}
