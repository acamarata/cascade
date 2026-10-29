package daemon

// Purpose: a Close that wins the race with the capacity step fails the build closed and
// leaves providers.db closed (windows cannot delete a file that is still open).
// SPORT: internal/daemon (ADD, P1-CAP-05).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestBuildCapacityCloseWinsLeavesNoOpenHandle(t *testing.T) {
	saved := afterRegistryDBOpen
	afterRegistryDBOpen = func(r *FleetRuntime) { _ = r.Close() }
	t.Cleanup(func() { afterRegistryDBOpen = saved })
	g := newFleetRig(t, "")
	r, err := NewFleetRuntime(g.ctx, g.deps)
	if k, _ := cascade.KindOf(err); err == nil || r != nil || k != cascade.KindUnavailable || !hasIdentity(err, errClosedDuringBuild) {
		t.Fatalf("NewFleetRuntime = (runtime %v), %v; want a KindUnavailable closed-during-build failure", r != nil, err)
	}
	g.cancel()
	g.awaitJoin(t)
	if err := os.Remove(filepath.Join(g.deps.Paths.DataDir(), "providers.db")); err != nil {
		t.Fatalf("delete providers.db after the failed build: %v (a handle leaked)", err)
	}
}
