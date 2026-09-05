package hooks

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// mapVault is a hermetic in-memory value source. It holds only what a
// test put in it, so no test reads a real vault (Art.7.1).
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
		return nil, cascade.Newf(cascade.KindNotFound, "test vault: %q", name)
	}
	return value, nil
}

// testEngine builds a real egress engine over a hermetic vault.
func testEngine(t *testing.T, values map[string][]byte) *egress.Engine {
	t.Helper()
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	engine, err := egress.NewEngine(egress.DefaultRegistry(), &mapVault{values: values}, detector)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return engine
}

// testFirewall returns the real firewall and the hook class capability
// the dispatcher requires.
func testFirewall(t *testing.T) (Interceptor, egress.Capability) {
	t.Helper()
	engine := testEngine(t, nil)
	token, err := egress.DefaultRegistry().Capability(egress.EgressClassHook)
	if err != nil {
		t.Fatalf("Capability(hook): %v", err)
	}
	return engine, token
}
