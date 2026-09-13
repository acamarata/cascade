package daemon

import (
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/rpc"
)

// TestRegisterFleetModeHandler_MountsMethods proves
// RegisterFleetModeHandler actually mounts fleet.mode.show/set on the
// registry it is given, over a real cascade.db file under t.TempDir --
// closing the same class of gap R-14.223 named for fleet.journal_show.
func TestRegisterFleetModeHandler_MountsMethods(t *testing.T) {
	registry := rpc.NewRegistry()
	paths := quotaTestPaths{root: t.TempDir()}
	clock := fixedDaemonClock{t: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)}

	db, err := RegisterFleetModeHandler(registry, paths, clock)
	if err != nil {
		t.Fatalf("RegisterFleetModeHandler: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if !registry.Registered(economics.MethodFleetModeShow) {
		t.Errorf("registry.Registered(%q) = false, want true", economics.MethodFleetModeShow)
	}
	if !registry.Registered(economics.MethodFleetModeSet) {
		t.Errorf("registry.Registered(%q) = false, want true", economics.MethodFleetModeSet)
	}
}

// TestRegisterFleetModeHandler_NilDegradesToNoop proves the documented
// nil-paths/nil-clock degradation.
func TestRegisterFleetModeHandler_NilDegradesToNoop(t *testing.T) {
	registry := rpc.NewRegistry()
	db, err := RegisterFleetModeHandler(registry, nil, fixedDaemonClock{t: time.Now()})
	if err != nil || db != nil {
		t.Fatalf("RegisterFleetModeHandler(nil paths) = (%v, %v), want (nil, nil)", db, err)
	}
	if registry.Registered(economics.MethodFleetModeShow) {
		t.Error("registry.Registered = true with nil paths, want false")
	}

	registry2 := rpc.NewRegistry()
	db2, err2 := RegisterFleetModeHandler(registry2, quotaTestPaths{root: t.TempDir()}, nil)
	if err2 != nil || db2 != nil {
		t.Fatalf("RegisterFleetModeHandler(nil clock) = (%v, %v), want (nil, nil)", db2, err2)
	}
}

// TestRegisterFleetModeHandler_IdempotentReapply proves the migration
// this registration applies is idempotent.
func TestRegisterFleetModeHandler_IdempotentReapply(t *testing.T) {
	paths := quotaTestPaths{root: t.TempDir()}
	clock := fixedDaemonClock{t: time.Now()}

	registry1 := rpc.NewRegistry()
	db1, err := RegisterFleetModeHandler(registry1, paths, clock)
	if err != nil {
		t.Fatalf("first RegisterFleetModeHandler: %v", err)
	}
	_ = db1.Close()

	registry2 := rpc.NewRegistry()
	db2, err := RegisterFleetModeHandler(registry2, paths, clock)
	if err != nil {
		t.Fatalf("second RegisterFleetModeHandler (re-apply) should be a no-op, got: %v", err)
	}
	_ = db2.Close()
}
