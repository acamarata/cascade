package egress

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the process firewall seam's refusals. An unset
// factory, a nil registry, a factory error and a factory that answers
// nothing must each refuse; only a factory that returns an engine yields
// one, and it yields exactly that engine.
// Constraints: no vault, no keychain, no environment. Every test restores
// the factory it found.

// swapProcessFactory installs f for the test and restores nil afterwards.
// Nothing in this package installs a factory, so nil is what it found.
func swapProcessFactory(t *testing.T, f func(*Registry) (*Engine, error)) {
	t.Helper()
	SetProcessEngineFactory(f)
	t.Cleanup(func() { SetProcessEngineFactory(nil) })
}

// stubVault is a test-only value source holding nothing.
type stubVault struct{}

func (stubVault) List(context.Context) ([]string, error)      { return nil, nil }
func (stubVault) Get(context.Context, string) ([]byte, error) { return nil, errors.New("absent") }

func TestProcessEngineUnsetRefuses(t *testing.T) {
	SetProcessEngineFactory(nil)
	engine, err := ProcessEngine(DefaultRegistry())
	if engine != nil {
		t.Fatalf("ProcessEngine with no factory returned an engine (%p); it must refuse", engine)
	}
	if err != ErrNoProcessFirewall { //nolint:errorlint // identity is the assertion; errors.Is compares Kind only here
		t.Fatalf("err = %v, want ErrNoProcessFirewall by identity", err)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err kind = %v, want KindUnavailable", err)
	}
	if !strings.Contains(err.Error(), "egress: no process firewall is installed") {
		t.Fatalf("err message = %q", err.Error())
	}
}

func TestProcessEngineReturnsTheFactoryEngine(t *testing.T) {
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	want, err := NewEngine(reg, stubVault{}, detector)
	if err != nil {
		t.Fatal(err)
	}
	var got *Registry
	swapProcessFactory(t, func(r *Registry) (*Engine, error) { got = r; return want, nil })
	engine, err := ProcessEngine(reg)
	if err != nil {
		t.Fatalf("ProcessEngine: %v", err)
	}
	if engine != want || got != reg {
		t.Fatalf("ProcessEngine returned %p over %p; want the factory's %p over %p", engine, got, want, reg)
	}
}

func TestProcessEngineFactoryErrorUnchanged(t *testing.T) {
	cause := cascade.New(cascade.KindIntegrity, "test: custody integrity failure")
	swapProcessFactory(t, func(*Registry) (*Engine, error) { return nil, cause })
	engine, err := ProcessEngine(DefaultRegistry())
	if engine != nil || err != cause { //nolint:errorlint // identity: the factory error must pass through unchanged
		t.Fatalf("ProcessEngine = (%p, %v), want (nil, the factory error unchanged)", engine, err)
	}
}

func TestProcessEngineRefusesNilRegistryAndNilEngine(t *testing.T) {
	calls := 0
	swapProcessFactory(t, func(*Registry) (*Engine, error) { calls++; return nil, nil })
	if _, err := ProcessEngine(nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil registry: err = %v, want KindInvalidInput", err)
	}
	if calls != 0 {
		t.Fatal("the factory ran for a nil registry")
	}
	engine, err := ProcessEngine(DefaultRegistry())
	if engine != nil || !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("a factory answering (nil, nil): got (%p, %v), want a KindInternal refusal", engine, err)
	}
}
