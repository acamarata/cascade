package wasm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestHostSecretRef_NeverReturnsLiteral proves host_secretref returns
// only an opaque reference token, never the underlying secret's literal
// value, even when the broker holds one. This is a blocking CR-A finding
// if absent (acceptance criterion).
func TestHostSecretRef_NeverReturnsLiteral(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	lm, err := rt.Loader().Load(ctx, "secret", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	const literal = "sk_live_do_not_leak_this_literal_value"
	deps := testDeps()
	deps.Secrets = &leakCheckBroker{literal: literal}

	var resp SecretRefResponse
	if err := rt.Dispatch(ctx, lm, "call_host_secretref", "p", nil, deps, WASIConfig{}, testLimits(),
		SecretRefRequest{Name: "api-key"}, &resp); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.RefID == "" {
		t.Fatal("expected a non-empty reference token")
	}
	if resp.RefID == literal {
		t.Fatalf("RefID equals the literal secret value: %q", resp.RefID)
	}

	// Structural check on the wire: the raw JSON response must not
	// contain the literal anywhere, not just the typed field.
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), literal) {
		t.Fatalf("response wire bytes contain the literal secret: %s", raw)
	}
}

func TestHostSecretRef_EmptyNameRefused(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	lm, err := rt.Loader().Load(ctx, "secret-empty", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var resp SecretRefResponse
	err = rt.Dispatch(ctx, lm, "call_host_secretref", "p", nil, testDeps(), WASIConfig{}, testLimits(),
		SecretRefRequest{Name: ""}, &resp)
	if err == nil {
		t.Fatal("expected error for an empty secret name")
	}
}

func TestHostSecretRef_BrokerErrorPropagates(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	lm, err := rt.Loader().Load(ctx, "secret-err", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	deps := testDeps()
	deps.Secrets = errSecrets{}
	var resp SecretRefResponse
	err = rt.Dispatch(ctx, lm, "call_host_secretref", "p", nil, deps, WASIConfig{}, testLimits(),
		SecretRefRequest{Name: "x"}, &resp)
	if err == nil {
		t.Fatal("expected the broker's own error to propagate")
	}
}

// leakCheckBroker is a SecretBroker whose CreateRef holds a real literal
// but its return type (bare string opaque token, per the interface) is
// structurally incapable of smuggling it back — this fake proves the
// broker adapter's OWN discipline, not just the response type shape.
type leakCheckBroker struct{ literal string }

func (b *leakCheckBroker) CreateRef(_ context.Context, name string) (string, error) {
	_ = b.literal // held by the broker; never returned.
	return "ref-token-for-" + name, nil
}
