package plugins

// Purpose (this file): unit coverage for cascadepa_install_dbevents.go's
//   lazy *events.Bus opener, split out of cascadepa_install_wiring_test.go
//   alongside its source file.
//
// REWORK (round-3, T0 decision D1): round-2's TestDBEventBus_
//   PathResolutionFailurePropagates drove sharedCascadeStore's own
//   resolvePaths-failure fallback, which round-3 deleted entirely (D1) --
//   dbEventBus's shared.open() call now resolves ONLY through injected
//   InstallHostDeps, with no path resolution of its own. Replaced by
//   TestDBEventBus_NoHostDepsRefuses, the direct D1 proof for this
//   collaborator.
// SPORT: internal/plugins:cascadepa-install-wiring (TEST) -- FIX P1-E24-W5-S50-T4 (D1, round 3).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestDBEventBus_NoHostDepsRefuses proves dbEventBus's Publish refuses with
// KindUnavailable when the daemon composition root has not injected a
// store -- it never falls back to opening one itself.
func TestDBEventBus_NoHostDepsRefuses(t *testing.T) {
	SetInstallHostDeps(nil)
	d := newDBEventBus(newSharedCascadeStore(), testkit.NewFrozenClock(fixedInstallTestTime))
	_, err := d.Publish(context.Background(), "ns", "kind", "src", nil)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Publish: err = %v, want KindUnavailable", err)
	}
}

func TestDBEventBus_OpensRealBusAndPublishes(t *testing.T) {
	dir := t.TempDir()
	hostStoreFixture(t, dir)
	d := newDBEventBus(newSharedCascadeStore(), testkit.NewFrozenClock(fixedInstallTestTime))
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Publish(context.Background(), "cascade-pa.install", "install_proposal", "cascade-pa/install", []byte(`{}`)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// Second call reuses the same lazily-opened *events.Bus (sync.Once) --
	// proves the lazy-open path itself, not just a fresh one each time.
	if _, err := d.Publish(context.Background(), "cascade-pa.install", "install_declined", "cascade-pa/install", []byte(`{}`)); err != nil {
		t.Fatalf("second Publish: %v", err)
	}
}

// TestDBEventBus_Close proves Close (round-1 CR fix item 11) actually
// releases the real handles this file opens: a Publish after Close fails
// on the Bus's own "Publish called after Close" refusal.
func TestDBEventBus_Close(t *testing.T) {
	dir := t.TempDir()
	hostStoreFixture(t, dir)
	d := newDBEventBus(newSharedCascadeStore(), testkit.NewFrozenClock(fixedInstallTestTime))
	if _, err := d.Publish(context.Background(), "cascade-pa.install", "install_proposal", "cascade-pa/install", []byte(`{}`)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := d.Publish(context.Background(), "cascade-pa.install", "install_proposal", "cascade-pa/install", []byte(`{}`)); err == nil {
		t.Fatal("Publish after Close: err = nil, want the closed-bus refusal")
	}
}

// TestDBEventBus_CloseBeforePublishIsNoOp proves Close on a dbEventBus that
// never opened anything does not panic or error.
func TestDBEventBus_CloseBeforePublishIsNoOp(t *testing.T) {
	d := newDBEventBus(newSharedCascadeStore(), testkit.NewFrozenClock(fixedInstallTestTime))
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v, want nil on a never-opened bus", err)
	}
}
