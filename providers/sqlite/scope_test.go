// Purpose: package-local tests for Driver.Scoped and its scopedStore/
//   scopedTx implementation (scope.go) — the domain-scope enforcement
//   surface P1-E02-W1-S02-T5 adds directly to this package. The richer
//   cross-domain-with-a-real-registry scenarios live in
//   internal/storage/capability_test.go (that package may import this
//   one; this package may never import internal/**, Art.10.2), so these
//   tests use a small local fakeChecker instead, focused on proving
//   scope.go's own call-routing and error-propagation is correct in
//   isolation from any particular GrantChecker implementation.
// SPORT: providers.sqlite.Driver/CHANGED (P1-E02-W1-S02-T5).

package sqlite_test

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

// fakeChecker is a minimal sqlite.GrantChecker double: it records every
// call and returns allowErr (nil = allow) unconditionally, so tests can
// drive both the granted and denied paths without a real capability
// registry.
type fakeChecker struct {
	allowErr error
	calls    []fakeCheckCall
}

type fakeCheckCall struct {
	src, dst string
	op       sqlite.CapOp
}

func (f *fakeChecker) Check(_ context.Context, src, dst string, op sqlite.CapOp) error {
	f.calls = append(f.calls, fakeCheckCall{src: src, dst: dst, op: op})
	return f.allowErr
}

var errFakeCheckerDenied = errors.New("fakeChecker: denied")

// TestDomainScope_SameDomainPassesThroughWithoutChecking proves a
// same-namespace call never reaches the checker at all — a domain needs
// no grant to touch its own data.
func TestDomainScope_SameDomainPassesThroughWithoutChecking(t *testing.T) {
	d := newTestDriver(t)
	checker := &fakeChecker{}
	store := d.Scoped("context", checker)
	ctx := context.Background()

	if err := store.Put(ctx, "context", "k", []byte("v")); err != nil {
		t.Fatalf("same-domain Put: %v", err)
	}
	got, err := store.Get(ctx, "context", "k")
	if err != nil || string(got) != "v" {
		t.Fatalf("same-domain Get = (%q, %v), want (\"v\", nil)", got, err)
	}
	if err := store.Delete(ctx, "context", "k"); err != nil {
		t.Fatalf("same-domain Delete: %v", err)
	}
	if len(checker.calls) != 0 {
		t.Errorf("checker.calls = %d, want 0 (same-domain calls must not invoke the checker)", len(checker.calls))
	}
}

// TestDomainScope_NilCheckerDeniesCrossDomain proves a nil GrantChecker
// fails closed rather than silently permitting cross-domain access.
func TestDomainScope_NilCheckerDeniesCrossDomain(t *testing.T) {
	d := newTestDriver(t)
	store := d.Scoped("context", nil)
	ctx := context.Background()

	if err := store.Put(ctx, "memory", "k", []byte("v")); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Errorf("Put with nil checker = %v, want a KindPermissionDenied error", err)
	}
	if _, err := store.Get(ctx, "memory", "k"); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Errorf("Get with nil checker = %v, want a KindPermissionDenied error", err)
	}
	if err := store.Delete(ctx, "memory", "k"); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Errorf("Delete with nil checker = %v, want a KindPermissionDenied error", err)
	}
	if _, err := store.Scan(ctx, "memory", ""); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Errorf("Scan with nil checker = %v, want a KindPermissionDenied error", err)
	}
}

// TestDomainScope_CheckerDenyPropagates proves a checker's own error is
// returned unchanged (never re-wrapped, never swallowed) on every guarded
// method, and that the checker is actually invoked with the (scoped
// domain, target namespace) pair.
func TestDomainScope_CheckerDenyPropagates(t *testing.T) {
	d := newTestDriver(t)
	checker := &fakeChecker{allowErr: errFakeCheckerDenied}
	store := d.Scoped("context", checker)
	ctx := context.Background()

	if err := store.Put(ctx, "memory", "k", []byte("v")); !errors.Is(err, errFakeCheckerDenied) {
		t.Errorf("Put denied = %v, want errFakeCheckerDenied", err)
	}
	if _, err := store.Get(ctx, "memory", "k"); !errors.Is(err, errFakeCheckerDenied) {
		t.Errorf("Get denied = %v, want errFakeCheckerDenied", err)
	}
	if err := store.Delete(ctx, "memory", "k"); !errors.Is(err, errFakeCheckerDenied) {
		t.Errorf("Delete denied = %v, want errFakeCheckerDenied", err)
	}
	if _, err := store.Scan(ctx, "memory", ""); !errors.Is(err, errFakeCheckerDenied) {
		t.Errorf("Scan denied = %v, want errFakeCheckerDenied", err)
	}

	wantOps := map[int]sqlite.CapOp{0: sqlite.CapOpWrite, 1: sqlite.CapOpRead, 2: sqlite.CapOpWrite, 3: sqlite.CapOpRead}
	if len(checker.calls) != len(wantOps) {
		t.Fatalf("checker.calls = %d, want %d", len(checker.calls), len(wantOps))
	}
	for i, want := range wantOps {
		c := checker.calls[i]
		if c.src != "context" || c.dst != "memory" || c.op != want {
			t.Errorf("checker.calls[%d] = %+v, want src=context dst=memory op=%v", i, c, want)
		}
	}
}

// TestDomainScope_CrossDomainAllowedWithGrant proves a checker that
// allows the request lets Get/Put/Delete/Scan all succeed, actually
// reading and writing real data through the underlying driver.
func TestDomainScope_CrossDomainAllowedWithGrant(t *testing.T) {
	d := newTestDriver(t)
	checker := &fakeChecker{}
	store := d.Scoped("context", checker)
	ctx := context.Background()

	if err := store.Put(ctx, "memory", "k", []byte("v")); err != nil {
		t.Fatalf("cross-domain Put with allowing checker: %v", err)
	}
	got, err := store.Get(ctx, "memory", "k")
	if err != nil || string(got) != "v" {
		t.Fatalf("cross-domain Get = (%q, %v), want (\"v\", nil)", got, err)
	}
	it, err := store.Scan(ctx, "memory", "")
	if err != nil {
		t.Fatalf("cross-domain Scan: %v", err)
	}
	defer func() { _ = it.Close() }()
	found := false
	for it.Next(ctx) {
		if it.Key() == "k" {
			found = true
		}
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if !found {
		t.Error("cross-domain Scan did not find the key just written")
	}
	if err := store.Delete(ctx, "memory", "k"); err != nil {
		t.Fatalf("cross-domain Delete: %v", err)
	}
}

// TestDomainScope_ForgedPrefixIsDistinctDomain proves the guard compares
// namespace to the scoped domain by exact equality: a namespace that
// merely starts with the scoped domain's name is still a distinct,
// checked namespace, not silently accepted as "close enough."
func TestDomainScope_ForgedPrefixIsDistinctDomain(t *testing.T) {
	d := newTestDriver(t)
	checker := &fakeChecker{allowErr: errFakeCheckerDenied}
	store := d.Scoped("context", checker)
	ctx := context.Background()

	if err := store.Put(ctx, "context-forged", "k", []byte("v")); !errors.Is(err, errFakeCheckerDenied) {
		t.Errorf("Put to \"context-forged\" = %v, want errFakeCheckerDenied (must not match scoped domain \"context\")", err)
	}
}

// TestDomainScope_TxEnforcesPerCall proves the guard applies inside a
// Store.Tx closure to Get/Put/Delete/CompareAndSwap alike, each call
// checked independently against the namespace it targets.
func TestDomainScope_TxEnforcesPerCall(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()

	// Seed a same-domain value directly (no checker needed).
	seedChecker := &fakeChecker{}
	seedStore := d.Scoped("context", seedChecker)
	if err := seedStore.Put(ctx, "context", "own", []byte("v0")); err != nil {
		t.Fatalf("seed Put: %v", err)
	}

	denyChecker := &fakeChecker{allowErr: errFakeCheckerDenied}
	denyStore := d.Scoped("context", denyChecker)
	err := denyStore.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		if _, err := tx.Get(ctx, "context", "own"); err != nil {
			t.Errorf("same-domain Tx.Get inside cross-domain Tx: %v", err)
		}
		return tx.Put(ctx, "memory", "k", []byte("v"))
	})
	if !errors.Is(err, errFakeCheckerDenied) {
		t.Fatalf("Tx.Put cross-domain, denied = %v, want errFakeCheckerDenied", err)
	}

	allowChecker := &fakeChecker{}
	allowStore := d.Scoped("context", allowChecker)
	err = allowStore.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		if err := tx.Put(ctx, "memory", "k", []byte("v1")); err != nil {
			return err
		}
		if err := tx.CompareAndSwap(ctx, "memory", "k", []byte("v1"), []byte("v2")); err != nil {
			return err
		}
		return tx.Delete(ctx, "memory", "k")
	})
	if err != nil {
		t.Fatalf("Tx cross-domain, allowed = %v, want nil", err)
	}
}
