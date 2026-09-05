package egress

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// mapVault is a hermetic value source: it holds exactly what a test put
// in it and never touches a real vault or the filesystem (Art.7.1).
type mapVault struct {
	values  map[string][]byte
	listErr error
	getErr  error
}

func (v *mapVault) List(context.Context) ([]string, error) {
	if v.listErr != nil {
		return nil, v.listErr
	}
	out := make([]string, 0, len(v.values))
	for name := range v.values {
		out = append(out, name)
	}
	return out, nil
}

func (v *mapVault) Get(_ context.Context, name string) ([]byte, error) {
	if v.getErr != nil {
		return nil, v.getErr
	}
	value, ok := v.values[name]
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound, "test vault: %q not stored", name)
	}
	return value, nil
}

// newDetector builds the default detector, failing the test rather than
// returning an error nobody checks.
func newDetector(t *testing.T) *secrets.Detector {
	t.Helper()
	d, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	return d
}

// newTestEngine builds an engine over the default class registrations and
// a hermetic vault.
func newTestEngine(t *testing.T, values map[string][]byte) *Engine {
	t.Helper()
	return newEngineOn(t, DefaultRegistry(), &mapVault{values: values})
}

// newEngineOn builds an engine over an explicit registry and vault.
func newEngineOn(t *testing.T, registry *Registry, vault Vault) *Engine {
	t.Helper()
	engine, err := NewEngine(registry, vault, newDetector(t))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return engine
}

// mustCapability issues a capability or fails the test.
func mustCapability(t *testing.T, engine *Engine, class EgressClass) Capability {
	t.Helper()
	token, err := engine.Capability(class)
	if err != nil {
		t.Fatalf("Capability(%q): %v", string(class), err)
	}
	return token
}
