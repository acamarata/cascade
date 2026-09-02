// Purpose: Bootstrap's optional GrantRegistry integration — proves it
//
//	self-grants exactly once per AllDomains entry, in order, and that a
//	Register failure aborts Bootstrap with a wrapped error rather than
//	being swallowed. Split from domains_test.go as a sibling file per
//	R-14.117 (Art.10.3 300-line cap; mechanical relocation, no behavior
//	change).
//
// SPORT: internal.storage.domains.Bootstrap/ADDED (P1-E02-W1-S03-T1).
package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
)

// fakeGrantRegistry is a minimal storage.GrantRegistry double: it records
// every Register call, and fails from failAt onward (0 = never fail) so
// tests can drive both Bootstrap's optional-integration success path and
// its abort-on-registry-error path.
type fakeGrantRegistry struct {
	registered []storage.DomainID
	failAt     int // fail on the (1-indexed) call number >= failAt; 0 = never
}

func (f *fakeGrantRegistry) Register(_ context.Context, domain storage.DomainID) error {
	f.registered = append(f.registered, domain)
	if f.failAt != 0 && len(f.registered) >= f.failAt {
		return errFakeGrantRegistry
	}
	return nil
}

var errFakeGrantRegistry = fmt.Errorf("fakeGrantRegistry: refused")

// TestBootstrap_GrantRegistry_SelfGrantPerDomain proves Bootstrap calls
// GrantRegistry.Register exactly once per AllDomains entry, in order, when
// a non-nil GrantRegistry is supplied — the "optional integration" path
// the ticket describes.
func TestBootstrap_GrantRegistry_SelfGrantPerDomain(t *testing.T) {
	db := openTestDB(t)
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	registry := &fakeGrantRegistry{}

	if _, err := storage.Bootstrap(context.Background(), db, storage.BootstrapOpts{Clock: clock, GrantRegistry: registry}); err != nil {
		t.Fatalf("Bootstrap with GrantRegistry: %v", err)
	}
	if len(registry.registered) != len(storage.AllDomains) {
		t.Fatalf("GrantRegistry.Register called %d times, want %d", len(registry.registered), len(storage.AllDomains))
	}
	for i, meta := range storage.AllDomains {
		if registry.registered[i] != meta.ID {
			t.Errorf("Register call %d = %q, want %q (self-grant order must match AllDomains)", i, registry.registered[i], meta.ID)
		}
	}
}

// TestBootstrap_GrantRegistry_ErrorAborts proves a GrantRegistry.Register
// failure aborts Bootstrap with a wrapped error rather than being
// swallowed.
func TestBootstrap_GrantRegistry_ErrorAborts(t *testing.T) {
	db := openTestDB(t)
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	registry := &fakeGrantRegistry{failAt: 2}

	_, err := storage.Bootstrap(context.Background(), db, storage.BootstrapOpts{Clock: clock, GrantRegistry: registry})
	if err == nil {
		t.Fatal("Bootstrap with a failing GrantRegistry: want error, got nil")
	}
}
