// Purpose: real providers/sqlite integration tests proving the
//
//	cross-domain guard actually blocks a read, a write, a scan, a forged
//	namespace, and a Tx closure — not merely a documented intent. Split
//	from capability_test.go as a sibling file per R-14.117 (Art.10.3
//	300-line cap). Every .db file lives in t.TempDir() (Art.7.1).
//
// SPORT: providers.sqlite.Driver/CHANGED (P1-E02-W1-S02-T5).
package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

// newScopedIsolationDriver opens one real providers/sqlite Driver in
// t.TempDir() and returns two domain-scoped provider.Store handles over
// it (domainA, domainB) sharing the one registryChecker adapter — proving
// the wiring shape a composition root would use, per capability.go's
// package doc.
func newScopedIsolationDriver(t *testing.T) (a, b provider.Store, reg *storage.CapabilityRegistry) {
	t.Helper()
	ctx := context.Background()
	d, err := sqlite.Open(ctx, t.TempDir()+"/cascade.db")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	reg = storage.NewCapabilityRegistry(testClock())
	checker := registryChecker{reg: reg}
	a = d.Scoped(string(storage.DomainContext), checker)
	b = d.Scoped(string(storage.DomainMemory), checker)
	return a, b, reg
}

// TestCapability_DomainIsolation_CrossDomainWriteDenied proves a Store
// handle scoped to domainA cannot write domainB's namespace without a
// grant, that registering the grant lets the write through, and that
// revoking it closes the door again — the ticket's own worked example
// (task 4), end to end against a real database.
func TestCapability_DomainIsolation_CrossDomainWriteDenied(t *testing.T) {
	ctx := context.Background()
	a, _, reg := newScopedIsolationDriver(t)

	err := a.Put(ctx, string(storage.DomainMemory), "k", []byte("v"))
	if !errors.Is(err, storage.ErrDomainForbidden) {
		t.Fatalf("cross-domain Put without grant = %v, want ErrDomainForbidden", err)
	}

	if err := reg.Grant(ctx, storage.Grant{SrcDomain: storage.DomainContext, DstDomain: storage.DomainMemory, Ops: storage.OpWrite}); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if err := a.Put(ctx, string(storage.DomainMemory), "k", []byte("v")); err != nil {
		t.Fatalf("cross-domain Put with grant = %v, want nil", err)
	}

	if err := reg.Revoke(ctx, storage.DomainContext, storage.DomainMemory); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	err = a.Put(ctx, string(storage.DomainMemory), "k2", []byte("v2"))
	if !errors.Is(err, storage.ErrDomainForbidden) {
		t.Fatalf("cross-domain Put after revoke = %v, want ErrDomainForbidden", err)
	}
}

// TestCapability_DomainIsolation_CrossDomainReadDenied proves the
// symmetric read guard: a Get for another domain's namespace is denied
// without a grant. domainB writes to its own namespace first (unguarded,
// same-domain), then domainA attempts to read it cross-domain.
func TestCapability_DomainIsolation_CrossDomainReadDenied(t *testing.T) {
	ctx := context.Background()
	a, b, _ := newScopedIsolationDriver(t)

	if err := b.Put(ctx, string(storage.DomainMemory), "secret", []byte("v")); err != nil {
		t.Fatalf("domainB writing its own namespace: %v", err)
	}

	_, err := a.Get(ctx, string(storage.DomainMemory), "secret")
	if !errors.Is(err, storage.ErrDomainForbidden) {
		t.Fatalf("cross-domain Get without grant = %v, want ErrDomainForbidden", err)
	}
}

// TestCapability_DomainIsolation_ScanNeverLeaks proves the classic hole:
// a cross-domain Scan is refused before a single row is read, and a key
// deliberately crafted to look like it belongs to domainA (embedding
// domainA's own name) is still governed by the namespace argument, never
// by key content.
func TestCapability_DomainIsolation_ScanNeverLeaks(t *testing.T) {
	ctx := context.Background()
	a, b, _ := newScopedIsolationDriver(t)

	if err := b.Put(ctx, string(storage.DomainMemory), "context/looks-like-domainA-key", []byte("v")); err != nil {
		t.Fatalf("domainB writing its own namespace: %v", err)
	}

	_, err := a.Scan(ctx, string(storage.DomainMemory), "")
	if !errors.Is(err, storage.ErrDomainForbidden) {
		t.Fatalf("cross-domain Scan without grant = %v, want ErrDomainForbidden", err)
	}

	// domainA's own scan of its own namespace must not see domainB's row.
	it, err := a.Scan(ctx, string(storage.DomainContext), "")
	if err != nil {
		t.Fatalf("same-domain Scan: %v", err)
	}
	defer func() { _ = it.Close() }()
	for it.Next(ctx) {
		t.Errorf("domainA's own-namespace scan unexpectedly saw key %q", it.Key())
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
}

// TestCapability_DomainIsolation_ForgedPrefixNotBypassed proves
// enforcement compares the namespace argument to the scoped domain by
// exact string equality, never a prefix match: a namespace that is
// domainA's name with a suffix appended is still a distinct, ungranted
// domain, not silently treated as "the same domain, roughly."
func TestCapability_DomainIsolation_ForgedPrefixNotBypassed(t *testing.T) {
	ctx := context.Background()
	a, _, _ := newScopedIsolationDriver(t)

	forged := string(storage.DomainContext) + "-forged"
	err := a.Put(ctx, forged, "k", []byte("v"))
	if !errors.Is(err, storage.ErrDomainForbidden) {
		t.Fatalf("Put to forged-prefix namespace %q = %v, want ErrDomainForbidden", forged, err)
	}
}

// TestCapability_DomainIsolation_TxEnforced proves the guard also applies
// inside a Store.Tx closure, which can address any namespace regardless of
// which domain's handle opened the transaction.
func TestCapability_DomainIsolation_TxEnforced(t *testing.T) {
	ctx := context.Background()
	a, _, reg := newScopedIsolationDriver(t)

	err := a.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return tx.Put(ctx, string(storage.DomainMemory), "k", []byte("v"))
	})
	if !errors.Is(err, storage.ErrDomainForbidden) {
		t.Fatalf("cross-domain Tx.Put without grant = %v, want ErrDomainForbidden", err)
	}

	if err := reg.Grant(ctx, storage.Grant{SrcDomain: storage.DomainContext, DstDomain: storage.DomainMemory, Ops: storage.OpWrite}); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	err = a.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return tx.Put(ctx, string(storage.DomainMemory), "k", []byte("v"))
	})
	if err != nil {
		t.Fatalf("cross-domain Tx.Put with grant = %v, want nil", err)
	}
}

// TestCapability_DomainIsolation_NilCheckerDeniesEverything proves a
// domain-scoped Store opened with a nil GrantChecker fails closed on
// every cross-domain call rather than silently proceeding unscoped.
func TestCapability_DomainIsolation_NilCheckerDeniesEverything(t *testing.T) {
	ctx := context.Background()
	d, err := sqlite.Open(ctx, t.TempDir()+"/cascade.db")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	store := d.Scoped(string(storage.DomainContext), nil)
	if err := store.Put(ctx, string(storage.DomainMemory), "k", []byte("v")); !errors.Is(err, storage.ErrDomainForbidden) {
		t.Errorf("Put with nil checker = %v, want ErrDomainForbidden (sqlite.ErrScopeDenied wraps the same Kind)", err)
	}
	if _, err := store.Get(ctx, string(storage.DomainMemory), "k"); !errors.Is(err, storage.ErrDomainForbidden) {
		t.Errorf("Get with nil checker = %v, want ErrDomainForbidden", err)
	}
	// Same-domain calls still work with a nil checker — no grant is ever
	// needed for a domain to touch its own data.
	if err := store.Put(ctx, string(storage.DomainContext), "k", []byte("v")); err != nil {
		t.Errorf("same-domain Put with nil checker = %v, want nil", err)
	}
}
