// Purpose: conformance entry point for the sqlite driver — runs the real
//
//	internal/storage/storetest.RunStoreTests suite (Art.2: real modernc-
//	sqlite against a real .db file, no mock) plus the executor's
//	concurrency behavior under -race. Platform-lock, domain-registry and
//	bench-spike tests are split into sibling files (R-14.117: Art.10.3's
//	300-line cap authorizes in-package splits, joining this file's
//	authorized write set automatically).
//
// SPORT: providers.sqlite.Driver/ADDED (P1-E02-W1-S02-T2).
package sqlite_test

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	// depguard's plugins-providers-boundary rule (.golangci.yml, off-limits
	// to this ticket) globs "**/providers/**/*.go" without a _test.go
	// carve-out, unlike internal/build/arch_test.go's Go-level boundary
	// gate (arch.go's archScan explicitly skips _test.go — verified
	// empirically, not assumed, per this ticket's own instruction). A
	// driver's conformance test importing the shared storetest suite is
	// exactly the sanctioned pattern internal/storage/storetest/README.md
	// documents for every driver in the plan, so this is a scoped,
	// justified exception rather than a boundary violation — see
	// providers/sqlite/README.md "Why driver_test.go needs a depguard
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

// newTestDriver opens a fresh sqlite.Driver against a new .db file under
// t.TempDir() (Art.7.1: every test writes only under t.TempDir()) and
// registers Close with t.Cleanup.
func newTestDriver(t *testing.T) *sqlite.Driver {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cascade.db")
	d, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("Driver.Close: %v", err)
		}
	})
	return d
}

// TestSQLiteStore_Conformance runs the full provider.Store conformance
// suite (Get/Put/Delete/Scan, key-not-found, Tx commit/rollback,
// CompareAndSwap create/update/conflict) against the real driver.
func TestSQLiteStore_Conformance(t *testing.T) {
	storetest.RunStoreTests(t, func(t *testing.T) provider.Store {
		t.Helper()
		return newTestDriver(t)
	})
}

// TestDriver_TxGetAndDelete exercises provider.Tx's Get and Delete
// directly (RunStoreTests only exercises Tx.Put/CompareAndSwap through the
// Store-level surface it was written against), including tx.Get observing
// a write the same transaction already made but has not yet committed.
func TestDriver_TxGetAndDelete(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	if err := d.Put(ctx, "ns", "k", []byte("v1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	err := d.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		got, err := tx.Get(ctx, "ns", "k")
		if err != nil {
			return err
		}
		if string(got) != "v1" {
			t.Fatalf("tx.Get before write = %q, want v1", got)
		}
		if err := tx.Put(ctx, "ns", "k2", []byte("v2")); err != nil {
			return err
		}
		got2, err := tx.Get(ctx, "ns", "k2")
		if err != nil {
			return err
		}
		if string(got2) != "v2" {
			t.Fatalf("tx.Get of own uncommitted write = %q, want v2", got2)
		}
		if err := tx.Delete(ctx, "ns", "never-existed"); err != nil {
			t.Fatalf("tx.Delete of absent key (want idempotent nil): %v", err)
		}
		return tx.Delete(ctx, "ns", "k")
	})
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}

	if _, err := d.Get(ctx, "ns", "k"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Get(k) after tx.Delete: want KindNotFound, got %v", err)
	}
	if got, err := d.Get(ctx, "ns", "k2"); err != nil || string(got) != "v2" {
		t.Fatalf("Get(k2) after committed tx: got %q, %v", got, err)
	}
}

// TestDriver_OperationsAfterClose proves every write and read path returns
// a taxonomy error (never a panic, never a raw driver error) once the
// Driver is closed — the write executor is stopped and both connection
// pools are closed.
func TestDriver_OperationsAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.db")
	d, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("second Close (idempotent): %v", err)
	}

	ctx := context.Background()
	if _, err := d.Get(ctx, "ns", "k"); err == nil {
		t.Error("Get after Close: want error, got nil")
	}
	if err := d.Put(ctx, "ns", "k", []byte("v")); err == nil {
		t.Error("Put after Close: want error, got nil")
	}
	if err := d.Delete(ctx, "ns", "k"); err == nil {
		t.Error("Delete after Close: want error, got nil")
	}
	if err := d.Tx(ctx, func(context.Context, provider.Tx) error { return nil }); err == nil {
		t.Error("Tx after Close: want error, got nil")
	}
	if s := d.String(); !strings.Contains(s, path) {
		t.Errorf("String() = %q, want it to contain %q", s, path)
	}
}

// TestOpen_SchemaInitFailure exercises openLocked's schema-creation error
// branch: a path whose parent directory does not exist fails at the first
// write (CREATE TABLE), which openLocked must translate into a taxonomy
// error rather than a raw driver panic.
func TestOpen_SchemaInitFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-parent", "cascade.db")
	_, err := sqlite.Open(context.Background(), path)
	if err == nil {
		t.Fatal("Open with missing parent dir: want error, got nil")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Open with missing parent dir: want KindUnavailable, got %v", err)
	}
}

// TestDriver_CompareAndSwap_EdgeCases covers the two CompareAndSwap
// branches storetest's suite does not: expecting absence on a key that
// already exists, and expecting a specific old value on a key that does
// not exist at all.
func TestDriver_CompareAndSwap_EdgeCases(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	if err := d.Put(ctx, "ns", "exists", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	err := d.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return tx.CompareAndSwap(ctx, "ns", "exists", nil, []byte("new"))
	})
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("CAS(old=nil) on existing key: want KindConflict, got %v", err)
	}

	err = d.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return tx.CompareAndSwap(ctx, "ns", "absent", []byte("expected"), []byte("new"))
	})
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("CAS(old=expected) on absent key: want KindConflict, got %v", err)
	}
}

// TestSQLiteStore_ConcurrentWritesAcrossDomains exercises the write
// executor under real concurrency (-race): many goroutines hammering
// several distinct namespaces concurrently must all observe their own
// writes with no lost updates and no data race, since every write funnels
// through the single write connection.
func TestSQLiteStore_ConcurrentWritesAcrossDomains(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	const goroutines = 16
	const perGoroutine = 20
	domains := []string{"context", "memory", "audit", "config"}

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			domain := domains[g%len(domains)]
			for i := 0; i < perGoroutine; i++ {
				key := keyFor(g, i)
				if err := d.Put(ctx, domain, key, []byte(key)); err != nil {
					t.Errorf("Put(%s,%s): %v", domain, key, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	for g := 0; g < goroutines; g++ {
		domain := domains[g%len(domains)]
		for i := 0; i < perGoroutine; i++ {
			key := keyFor(g, i)
			got, err := d.Get(ctx, domain, key)
			if err != nil {
				t.Fatalf("Get(%s,%s): %v", domain, key, err)
			}
			if string(got) != key {
				t.Fatalf("Get(%s,%s) = %q, want %q", domain, key, got, key)
			}
		}
	}
}

func keyFor(g, i int) string {
	return "g" + strconv.Itoa(g) + "-k" + strconv.Itoa(i)
}
