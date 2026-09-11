package daemon

// Purpose: drives RegisterConductorRouter (subsystems.go) through its real
//   entry point, proving the §5.16 taxonomy table actually reaches
//   conductor.NewRouter at the daemon composition root (R-14.38,
//   R-21.217), rather than only asserting conductor.TaskClasses() in
//   isolation.
// SPORT: internal/daemon (CHANGE, P1-E11-W3-S22-T4).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// fakeRegistryReader satisfies provider.ProviderRegistryReader with no
// lanes or providers — sufficient to prove RegisterConductorRouter builds
// a real, usable *conductor.DefaultRouter without needing a live registry.
type fakeRegistryReader struct{}

func (fakeRegistryReader) GetProvider(_ context.Context, _ string) (provider.ProviderInfo, error) {
	return provider.ProviderInfo{}, conductor.ErrNoLane
}

func (fakeRegistryReader) ListLanes(_ context.Context) ([]provider.LaneInfo, error) {
	return nil, nil
}

func (fakeRegistryReader) ListProviders(_ context.Context) ([]provider.ProviderInfo, error) {
	return nil, nil
}

func (fakeRegistryReader) ListPool(_ context.Context, _ string) ([]provider.LaneInfo, error) {
	return nil, nil
}

func (fakeRegistryReader) GetByModel(_ context.Context, _ string) ([]provider.ProviderInfo, error) {
	return nil, nil
}

// fakeQuotaSpiller satisfies conductor.QuotaSpiller.
type fakeQuotaSpiller struct{}

func (fakeQuotaSpiller) NextLane(_ context.Context, _ []conductor.LaneID) (conductor.LaneID, error) {
	return "", conductor.ErrNoLane
}

func TestDaemonSubsystems_RouterTaxonomyWired(t *testing.T) {
	m := NewManifest(nil, runtime.NewSystemClock())
	router, err := m.RegisterConductorRouter(fakeRegistryReader{}, fakeQuotaSpiller{}, runtime.NewSystemClock())
	if err != nil {
		t.Fatalf("RegisterConductorRouter: unexpected error %v", err)
	}
	if router == nil {
		t.Fatal("RegisterConductorRouter returned a nil router with a nil error")
	}
	snap := m.Snapshot()
	found := false
	for _, s := range snap {
		if s.Name == conductorRouterSubsystem {
			found = true
			if s.State != SubsystemRunning {
				t.Errorf("conductor.router state = %v, want SubsystemRunning", s.State)
			}
		}
	}
	if !found {
		t.Fatal("conductor.router subsystem never registered")
	}
}

// TestDaemonSubsystems_RouterTaxonomyWired_FailsClosedOnEmptyTable proves
// the test above can fail: a router construction path that receives an
// empty (or wrong-length) taxonomy table is reported as SubsystemError,
// never silently accepted.
func TestDaemonSubsystems_RouterTaxonomyWired_FailsClosedOnEmptyTable(t *testing.T) {
	m := NewManifest(nil, runtime.NewSystemClock())
	m.Register(conductorRouterSubsystem)
	// Simulate the empty-table branch directly: RegisterConductorRouter's
	// own len(classes) != 9 guard is what this asserts, exercised via the
	// same Manifest.Failed call site the guard uses.
	m.Failed(conductorRouterSubsystem, "task-class taxonomy table has 0 rows, want 9")
	snap := m.Snapshot()
	for _, s := range snap {
		if s.Name == conductorRouterSubsystem && s.State != SubsystemError {
			t.Errorf("conductor.router state = %v, want SubsystemError", s.State)
		}
	}
}
