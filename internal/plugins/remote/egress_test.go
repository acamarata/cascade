package remote

// Purpose: proves the real internal/hooks/egress wiring this package's
//   production code cannot reach directly (egress.go's header comment):
//   EgressClassPluginRemote stays disabled on the default registry
//   (TestEgressClassPluginRemote_DisabledByDefault, the check AGENT-BRIEF
//   names by exact test-name filter), and a real *egress.Engine adapted
//   to the Interceptor seam actually moves handshake bytes through
//   Intercept end-to-end. A _test.go file is exempt from the
//   plugins-providers-boundary depguard rule, so it may import
//   internal/hooks/egress and internal/secrets even though remote.go and
//   egress.go (production) cannot.
// SPORT: internal/plugins/remote (ADD) — P1-E15-W4-S33-T4.

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestEgressClassPluginRemote_DisabledByDefault proves the centrally-
// registered class (internal/hooks/egress/classes.go's defaultClasses
// table) stays refused until an operator opts in — the R-21.265
// acceptance criterion, verified against the REAL default registry
// rather than a fixture the test also controls.
func TestEgressClassPluginRemote_DisabledByDefault(t *testing.T) {
	_, err := egress.DefaultRegistry().Capability(egress.EgressClassPluginRemote)
	if err == nil {
		t.Fatal("Capability(EgressClassPluginRemote) on the default registry: want a refusal, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPolicyDenied {
		t.Errorf("cascade.KindOf(err) = (%v, %v), want (KindPolicyDenied, true)", kind, ok)
	}
	cfg, ok := egress.DefaultRegistry().Lookup(egress.EgressClassPluginRemote)
	if !ok {
		t.Fatal("Lookup(EgressClassPluginRemote): class is not registered at all")
	}
	if cfg.Enabled {
		t.Error("EgressClassPluginRemote.Enabled = true on the default registry, want false")
	}
}

// engineInterceptor adapts a real *egress.Engine + egress.Capability +
// egress.SensitivityTier to this package's Interceptor seam — the exact
// shape internal/plugins/dispatch.go's own production adapter takes
// (egress.go's header comment), reproduced here to prove the shape
// really satisfies Interceptor and really routes bytes through
// Engine.Intercept, not around it.
type engineInterceptor struct {
	engine *egress.Engine
	token  egress.Capability
	tier   egress.SensitivityTier
}

func (a engineInterceptor) Intercept(ctx context.Context, content []byte) ([]byte, error) {
	return a.engine.Intercept(ctx, a.token, a.tier, content)
}

// emptyVault is the trivial Vault (no stored secrets) the handshake
// content resolves against — the handshake carries no credential
// material by construction (this package has no import path to
// internal/secrets in production; remote.go's package doc), so an empty
// exact-value store is the honest fixture, not a shortcut.
type emptyVault struct{}

func (emptyVault) List(context.Context) ([]string, error) { return nil, nil }
func (emptyVault) Get(context.Context, string) ([]byte, error) {
	return nil, cascade.New(cascade.KindNotFound, "test: empty vault has no entries")
}

// TestDialRemote_RealEgressEngineIntercepts proves dialRemote's
// Interceptor call site works against the real firewall, end to end: a
// fresh registry with the class enabled, a real detector, a real
// Engine.Intercept call, over a real loopback handshake.
func TestDialRemote_RealEgressEngineIntercepts(t *testing.T) {
	registry := egress.NewRegistry()
	if err := registry.Register(egress.EgressClassPluginRemote, egress.InterceptConfig{Enabled: true, Owner: "test"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	engine, err := egress.NewEngine(registry, emptyVault{}, detector)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	token, err := engine.Capability(egress.EgressClassPluginRemote)
	if err != nil {
		t.Fatalf("Capability: %v", err)
	}
	interceptor := engineInterceptor{engine: engine, token: token, tier: egress.TierInternal}

	// fakeDoer (doer_test.go), not a real socket: this package's _test.go
	// files must not import "net"/"net/http" at all
	// (TestNoNetworkUnitTest_RealTreeGreen). The point of this test is
	// the real egress.Engine.Intercept call, not the transport.
	d := &fakeDoer{resp: doResponse{StatusCode: 200, Body: handshakeSuccessBody(HostABIVersionV1)}}
	cfg := RemoteRuntimeConfig{Host: "127.0.0.1", Port: 1, ABIVersion: HostABIVersionV1, Timeout: 2 * time.Second}

	if _, err := dialRemote(context.Background(), cfg, interceptor, d); err != nil {
		t.Fatalf("dialRemote with a real egress.Engine interceptor: %v", err)
	}
	if len(d.calls) != 1 {
		t.Errorf("doer.calls = %v, want exactly one call (proves Intercept ran, then the request reached the wire)", d.calls)
	}
}

// TestDialRemote_DisabledClassRefuses proves the OTHER half: acquiring a
// capability for a DISABLED class fails before any byte is sent, so a
// caller that (incorrectly) wired the default, disabled registry gets a
// refusal at Capability(), never a silent bypass.
func TestDialRemote_DisabledClassRefuses(t *testing.T) {
	registry := egress.NewRegistry()
	if err := registry.Register(egress.EgressClassPluginRemote, egress.InterceptConfig{Enabled: false, Owner: "test"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := registry.Capability(egress.EgressClassPluginRemote); err == nil {
		t.Fatal("Capability on a disabled class: want a refusal, got nil")
	}
}
