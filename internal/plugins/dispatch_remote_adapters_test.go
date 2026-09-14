package plugins

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): cover the remote-handshake egress adapters — the
//   Interceptor that puts the real firewall in front of handshake bytes,
//   and the deliberately empty vault behind it. The vault's emptiness is a
//   DISCLOSED gap in dispatch_remote.go, and a disclosed gap with no test
//   is indistinguishable from an undisclosed one.
// SPORT: internal/plugins remote-dispatch-adapter tests (ADD).

// enabledRemoteEngine builds a real egress Engine over a fresh registry
// with the remote plugin class enabled, plus the capability token for it.
// Fresh rather than DefaultRegistry(): the process-wide registry is shared
// state, and a test that enabled a disabled-by-default class on it would
// change what every other test in the binary observes.
func enabledRemoteEngine(t *testing.T) (*egress.Engine, egress.Capability) {
	t.Helper()
	reg := egress.NewRegistry()
	if err := reg.Register(egress.EgressClassPluginRemote, egress.InterceptConfig{
		Enabled:      true,
		AllowedTiers: []egress.SensitivityTier{dispatchRemoteTier},
		Owner:        "dispatch_remote_adapters_test",
	}); err != nil {
		t.Fatalf("register the remote class: %v", err)
	}
	token, err := reg.Capability(egress.EgressClassPluginRemote)
	if err != nil {
		t.Fatalf("acquire the class capability: %v", err)
	}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("build the detector: %v", err)
	}
	engine, err := egress.NewEngine(reg, dispatchRemoteEmptyVault{}, detector)
	if err != nil {
		t.Fatalf("build the engine: %v", err)
	}
	return engine, token
}

// TestDispatchRemoteInterceptor_PassesCleanContent proves the adapter
// actually reaches the real firewall and returns what it decided, rather
// than quietly returning its input.
func TestDispatchRemoteInterceptor_PassesCleanContent(t *testing.T) {
	engine, token := enabledRemoteEngine(t)
	interceptor := dispatchRemoteInterceptor{engine: engine, token: token}

	body := []byte(`{"jsonrpc":"2.0","method":"handshake"}`)
	got, err := interceptor.Intercept(context.Background(), body)
	if err != nil {
		t.Fatalf("Intercept on clean handshake bytes: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("Intercept returned %q, want the handshake bytes unchanged", got)
	}
}

// TestDispatchRemoteInterceptor_CredentialNeverSurvives is the assertion
// that makes the one above meaningful: without it, an Intercept that
// returned its input unconditionally would pass every other test here.
//
// Both outcomes are asserted, so neither branch can pass vacuously. What
// must never happen is the credential reaching the caller — whether the
// firewall redacts it or refuses the write entirely.
//
// RECORDED BEHAVIOUR (verified 2026-09-14): this input takes the REFUSAL
// branch, and that is deliberate rather than a defect. The engine reports
// that the detector pass could not be applied and nothing is written,
// because the "high-entropy" class has no tag type. internal/secrets'
// rewriter documents the reasoning directly: a corroborated high-entropy
// span is refused rather than emitted with the span still in it, since the
// alternative is inventing a tag type for a span whose kind the detector
// could not name and rehydrating it later as something it may not be.
//
// The consequence is worth knowing: the remote handshake interceptor
// refuses ANY body carrying such a span, including innocuous material like
// an opaque id or a base64 blob. That is fail-closed, which is the right
// direction for a firewall, so it is recorded here rather than filed.
func TestDispatchRemoteInterceptor_CredentialNeverSurvives(t *testing.T) {
	engine, token := enabledRemoteEngine(t)
	interceptor := dispatchRemoteInterceptor{engine: engine, token: token}

	secret := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	body := []byte("AWS_SECRET_ACCESS_KEY=" + secret)
	got, err := interceptor.Intercept(context.Background(), body)
	if err != nil {
		if len(got) != 0 {
			t.Fatalf("the firewall refused but still returned %q; a refusal must write nothing", got)
		}
		return
	}
	if strings.Contains(string(got), secret) {
		t.Fatalf("the firewall passed a credential through untouched: %q", got)
	}
	if string(got) == string(body) {
		t.Fatalf("the firewall returned its input unchanged; nothing was applied: %q", got)
	}
}

// TestDispatchRemoteInterceptorRefusesADisabledClass proves the
// fail-closed contract newDispatchRemoteInterceptor documents: the remote
// class is disabled unless an operator re-registered it, and
// Capability() refuses a disabled class rather than handing out a token.
func TestDispatchRemoteInterceptorRefusesADisabledClass(t *testing.T) {
	reg := egress.NewRegistry()
	if err := reg.Register(egress.EgressClassPluginRemote, egress.InterceptConfig{
		Enabled: false,
		Owner:   "dispatch_remote_adapters_test",
	}); err != nil {
		t.Fatalf("register the disabled class: %v", err)
	}
	if _, err := reg.Capability(egress.EgressClassPluginRemote); err == nil {
		t.Fatal("Capability handed out a token for a disabled class; the remote runtime would be reachable by default")
	}
}

// TestDispatchRemoteEmptyVaultIsEmptyAndSaysSo pins the disclosed gap. The
// vault is deliberately empty, and that emptiness must read as a typed
// not-found rather than an empty value the exact-match pass would treat as
// a legitimate secret to compare against.
func TestDispatchRemoteEmptyVaultIsEmptyAndSaysSo(t *testing.T) {
	vault := dispatchRemoteEmptyVault{}

	names, err := vault.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("List returned %v, want no entries", names)
	}

	value, err := vault.Get(context.Background(), "anything")
	if err == nil {
		t.Fatal("Get returned a value from a vault documented as always empty")
	}
	if value != nil {
		t.Fatalf("Get returned %q alongside its error, want nil", value)
	}
	if got, ok := cascade.KindOf(err); !ok || got != cascade.KindNotFound {
		t.Fatalf("error kind = %v (typed=%v), want %v", got, ok, cascade.KindNotFound)
	}
}
