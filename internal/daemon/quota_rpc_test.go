package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

type quotaTestPaths struct{ root string }

func (p quotaTestPaths) Root() string       { return p.root }
func (p quotaTestPaths) ConfigPath() string { return filepath.Join(p.root, "config.toml") }
func (p quotaTestPaths) SocketPath() string { return filepath.Join(p.root, "daemon.sock") }
func (p quotaTestPaths) DataDir() string    { return filepath.Join(p.root, "data") }
func (p quotaTestPaths) LogDir() string     { return filepath.Join(p.root, "logs") }
func (p quotaTestPaths) StorageRoot(prof runtime.Profile) string {
	return filepath.Join(p.root, "data", "storage", string(prof))
}

type fixedDaemonClock struct{ t time.Time }

func (c fixedDaemonClock) Now() time.Time { return c.t }

// TestRegisterFleetQuotaHandler_MountsMethod proves
// RegisterFleetQuotaHandler actually mounts fleet.quota.snapshot on the
// registry it is given, over a real cascade.db file under t.TempDir --
// closing the same class of gap R-14.223 named for fleet.journal_show.
func TestRegisterFleetQuotaHandler_MountsMethod(t *testing.T) {
	registry := rpc.NewRegistry()
	paths := quotaTestPaths{root: t.TempDir()}
	clock := fixedDaemonClock{t: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)}

	db, err := RegisterFleetQuotaHandler(registry, paths, clock)
	if err != nil {
		t.Fatalf("RegisterFleetQuotaHandler: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if !registry.Registered(topology.MethodFleetQuotaSnapshot) {
		t.Errorf("registry.Registered(%q) = false, want true", topology.MethodFleetQuotaSnapshot)
	}
}

// TestRegisterFleetQuotaHandler_NilDegradesToNoop proves the documented
// nil-paths/nil-clock degradation: neither opens a database at a guessed
// path nor registers the method.
func TestRegisterFleetQuotaHandler_NilDegradesToNoop(t *testing.T) {
	registry := rpc.NewRegistry()
	db, err := RegisterFleetQuotaHandler(registry, nil, fixedDaemonClock{t: time.Now()})
	if err != nil || db != nil {
		t.Fatalf("RegisterFleetQuotaHandler(nil paths) = (%v, %v), want (nil, nil)", db, err)
	}
	if registry.Registered(topology.MethodFleetQuotaSnapshot) {
		t.Error("registry.Registered = true with nil paths, want false")
	}

	registry2 := rpc.NewRegistry()
	db2, err2 := RegisterFleetQuotaHandler(registry2, quotaTestPaths{root: t.TempDir()}, nil)
	if err2 != nil || db2 != nil {
		t.Fatalf("RegisterFleetQuotaHandler(nil clock) = (%v, %v), want (nil, nil)", db2, err2)
	}
}

// TestRegisterFleetQuotaHandler_IdempotentReapply proves the migration
// this registration applies is idempotent, matching every sibling
// registerXHandler's contract.
func TestRegisterFleetQuotaHandler_IdempotentReapply(t *testing.T) {
	paths := quotaTestPaths{root: t.TempDir()}
	clock := fixedDaemonClock{t: time.Now()}

	registry1 := rpc.NewRegistry()
	db1, err := RegisterFleetQuotaHandler(registry1, paths, clock)
	if err != nil {
		t.Fatalf("first RegisterFleetQuotaHandler: %v", err)
	}
	_ = db1.Close()

	registry2 := rpc.NewRegistry()
	db2, err := RegisterFleetQuotaHandler(registry2, paths, clock)
	if err != nil {
		t.Fatalf("second RegisterFleetQuotaHandler (re-apply) should be a no-op, got: %v", err)
	}
	_ = db2.Close()
}
