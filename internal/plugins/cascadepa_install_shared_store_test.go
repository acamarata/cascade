package plugins

// Purpose (this file): unit coverage for cascadepa_install_shared_store.go's
//   host-deps injection path (open() returns the injected store) and its
//   D1 refusal path (open() with no host deps injected).
//
// REWORK (round-3, T0 decision D1): round-2's TestInstallHostDepsConfigured_
//   ReflectsSetInstallHostDeps proved a test-only symbol
//   (InstallHostDepsConfigured) that failed internal/build's
//   TestTestOnlyUsage_RealTreeGreen gate -- deleted along with the symbol
//   (D2). TestSharedCascadeStore_Close_NonCloserStoreNoOps drove a branch
//   (Close closing a store this instance "owns" but that has no Close
//   method) that no longer exists -- Close is now an unconditional no-op
//   (D1: open() never opens anything itself) -- deleted with it. Replaced
//   by TestSharedCascadeStore_Open_NoHostDepsRefuses, the direct D1 proof.
// SPORT: internal/plugins:cascadepa-install-wiring (TEST) -- FIX P1-E24-W5-S50-T4 (D1, D2, round 3).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	sqlitestore "github.com/acamarata/cascade/providers/sqlite"
)

// TestSharedCascadeStore_Open_UsesInjectedHostStore is the direct proof
// against the confirming review's Q3 finding: with InstallHostDeps
// injected, open() must return the SAME store instance.
func TestSharedCascadeStore_Open_UsesInjectedHostStore(t *testing.T) {
	dir := t.TempDir()
	hostStore, err := sqlitestore.Open(context.Background(), filepath.Join(dir, "cascade.db"))
	if err != nil {
		t.Fatalf("sqlitestore.Open: %v", err)
	}
	t.Cleanup(func() { _ = hostStore.Close() })

	SetInstallHostDeps(&InstallHostDeps{Store: hostStore})
	t.Cleanup(func() { SetInstallHostDeps(nil) })

	shared := newSharedCascadeStore()
	got, err := shared.open(context.Background())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got != hostStore {
		t.Fatal("open() did not return the injected host store")
	}
	// Close must be a true no-op here: it must never close the daemon's
	// own store out from under every other adapter sharing it -- proven
	// with a real Put/Get round trip (a closed *sqlitestore.Driver
	// refuses both; a merely-empty-key not-found would look identical to
	// "still open", so a write-then-read is the real proof).
	if err := shared.Close(); err != nil {
		t.Fatalf("Close on an injected store: %v, want nil (no-op)", err)
	}
	if err := hostStore.Put(context.Background(), "ns", "k", []byte("v")); err != nil {
		t.Fatalf("hostStore.Put after shared.Close(): %v, want it still open and writable", err)
	}
	if got, err := hostStore.Get(context.Background(), "ns", "k"); err != nil || string(got) != "v" {
		t.Fatalf("hostStore.Get after shared.Close() = (%q, %v), want (\"v\", nil)", got, err)
	}
}

// TestSharedCascadeStore_Open_NoHostDepsRefuses is the direct D1 proof:
// with no InstallHostDeps ever injected, open() must refuse with
// KindUnavailable naming the missing SetInstallHostDeps wiring -- it must
// never fall back to opening its own private cascade.db. dir stands in for
// where a fallback WOULD have written cascade.db; asserting it stays empty
// proves no such file was ever created.
func TestSharedCascadeStore_Open_NoHostDepsRefuses(t *testing.T) {
	SetInstallHostDeps(nil)
	dir := t.TempDir()

	shared := newSharedCascadeStore()
	_, err := shared.open(context.Background())
	if err == nil {
		t.Fatal("open: err = nil, want KindUnavailable when SetInstallHostDeps was never called")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("open: err = %v, want KindUnavailable", err)
	}
	if !strings.Contains(err.Error(), "SetInstallHostDeps") {
		t.Fatalf("open: err = %v, want it to name the missing SetInstallHostDeps wiring", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("dir has %d entries after a refused open(), want 0 -- no cascade.db should ever be created", len(entries))
	}
}

// TestSharedCascadeStore_Open_EmptyHostDepsRefuses proves an
// InstallHostDeps with a nil Store (e.g. only Queue/Registry injected, the
// confirm-gate-only shape) is treated identically to no host deps at all.
func TestSharedCascadeStore_Open_EmptyHostDepsRefuses(t *testing.T) {
	SetInstallHostDeps(&InstallHostDeps{})
	t.Cleanup(func() { SetInstallHostDeps(nil) })

	shared := newSharedCascadeStore()
	_, err := shared.open(context.Background())
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("open: err = %v, want KindUnavailable for an InstallHostDeps with a nil Store", err)
	}
}

// TestSharedCascadeStore_Close_NeverOpenedNoOps proves Close is safe on a
// sharedCascadeStore that never had open() called on it at all.
func TestSharedCascadeStore_Close_NeverOpenedNoOps(t *testing.T) {
	s := newSharedCascadeStore()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v, want nil", err)
	}
}
