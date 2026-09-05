package egress

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestEgressCapabilityRequired is the choke-point proof: a call with no
// registry-issued capability is refused, an unknown class is refused
// before any content is examined, and a disabled class egresses nothing.
func TestEgressCapabilityRequired(t *testing.T) {
	engine := newTestEngine(t, nil)
	ctx := context.Background()
	content := []byte("payload")

	out, err := engine.Intercept(ctx, Capability{}, TierPublic, content)
	if !errors.Is(err, ErrCapabilityRequired) {
		t.Fatalf("a zero capability gave %v, want ErrCapabilityRequired", err)
	}
	if out != nil {
		t.Fatalf("a zero capability returned %d bytes", len(out))
	}
	if !cascade.HasKind(err, cascade.KindCapabilityDenied) {
		t.Fatalf("a zero capability gave kind %v, want capability-denied", err)
	}

	if _, err := engine.InterceptClass(ctx, "not.registered", TierPublic, content); !errors.Is(err, ErrUnknownClass) {
		t.Fatalf("an unregistered class gave %v, want ErrUnknownClass", err)
	}
	if _, err := engine.InterceptClass(ctx, EgressClassTelemetry, TierPublic, content); !errors.Is(err, ErrClassDisabled) {
		t.Fatalf("a disabled class gave %v, want ErrClassDisabled", err)
	}
}

// TestInterceptRefusesUnknownClassBeforeReadingContent proves the
// ordering claim: a refusal for an unknown class happens with the vault
// never read, so an unauthorised caller cannot make the firewall touch
// what it was asked to send.
func TestInterceptRefusesUnknownClassBeforeReadingContent(t *testing.T) {
	counting := &countingVault{}
	engine := newEngineOn(t, DefaultRegistry(), counting)
	token := Capability{class: "vanished.class"}
	if _, err := engine.Intercept(context.Background(), token, TierPublic, []byte("payload")); !errors.Is(err, ErrUnknownClass) {
		t.Fatalf("got %v, want ErrUnknownClass", err)
	}
	if counting.lists != 0 {
		t.Fatalf("the vault was read %d times for an unknown class; want 0", counting.lists)
	}
}

// TestInterceptRefusesSensitivityBeforeReadingContent proves the same for
// a tier refusal.
func TestInterceptRefusesSensitivityBeforeReadingContent(t *testing.T) {
	counting := &countingVault{}
	engine := newEngineOn(t, DefaultRegistry(), counting)
	out, err := engine.InterceptClass(context.Background(), EgressClassMCP, TierLocalOnly, []byte("payload"))
	if !errors.Is(err, ErrSensitivityViolation) {
		t.Fatalf("got %v, want ErrSensitivityViolation", err)
	}
	if out != nil {
		t.Fatalf("a refused tier returned %d bytes", len(out))
	}
	if counting.lists != 0 {
		t.Fatalf("the vault was read %d times for a refused tier; want 0", counting.lists)
	}
}

// TestNilEngineRefuses covers the zero-value engine: it admits nothing.
func TestNilEngineRefuses(t *testing.T) {
	var engine *Engine
	if _, err := engine.Capability(EgressClassMCP); err == nil {
		t.Fatal("a nil engine issued a capability")
	}
	if _, err := engine.Intercept(context.Background(), Capability{}, TierPublic, nil); err == nil {
		t.Fatal("a nil engine intercepted")
	}
	if _, err := engine.InterceptClass(context.Background(), EgressClassMCP, TierPublic, nil); err == nil {
		t.Fatal("a nil engine intercepted by class")
	}
	empty := &Engine{}
	if _, err := empty.Intercept(context.Background(), Capability{}, TierPublic, nil); err == nil {
		t.Fatal("a zero engine intercepted")
	}
	if _, err := empty.Capability(EgressClassMCP); err == nil {
		t.Fatal("a zero engine issued a capability")
	}
	if _, err := empty.InterceptClass(context.Background(), EgressClassMCP, TierPublic, nil); err == nil {
		t.Fatal("a zero engine intercepted by class")
	}
	if engine.Registry() != nil {
		t.Fatal("a nil engine reported a registry")
	}
}

// TestEngineRegistryRoundTrip covers the accessor.
func TestEngineRegistryRoundTrip(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegister("only", InterceptConfig{Enabled: true, Owner: "t"})
	engine := newEngineOn(t, registry, &mapVault{})
	if engine.Registry() != registry {
		t.Fatal("Registry() returned a different registry")
	}
	if _, err := engine.Capability("only"); err != nil {
		t.Fatalf("Capability on the engine's own registry: %v", err)
	}
}

// countingVault records how many times it was listed.
type countingVault struct {
	lists int
}

func (v *countingVault) List(context.Context) ([]string, error) {
	v.lists++
	return nil, nil
}

func (v *countingVault) Get(context.Context, string) ([]byte, error) {
	return nil, cascade.New(cascade.KindNotFound, "countingVault holds nothing")
}
