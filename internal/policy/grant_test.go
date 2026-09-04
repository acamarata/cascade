// Purpose: the grant model's contract over a REAL B-layer store —
// providers/sqlite on a t.TempDir() file (Art.2, Art.7.1), never an
// in-memory double — covering the round trip, persistence across a
// simulated daemon restart, immediate revocation, and expiry against an
// injected clock.
//
// SPORT: internal/policy Grant/ADDED, StoreGrants/ADDED
// (P1-E09-W2-S17-T1).
package policy

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

// grantFixture bundles the pieces a grant test needs, over a real
// database file that survives close and reopen.
type grantFixture struct {
	path  string
	db    *sqlite.Driver
	reg   *MemoryRegistry
	clock *testkit.FrozenClock
	store *StoreGrants
}

// baseTime is the frozen instant every grant test measures from.
var baseTime = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

// testSubject is the subject most cases grant to.
func testSubject() Subject { return Subject{Kind: SubjectAgent, ID: "lane-a"} }

// newGrantFixture opens a real SQLite database in a temp dir, registers
// one capability, and builds a StoreGrants over it.
func newGrantFixture(t *testing.T) *grantFixture {
	t.Helper()
	f := &grantFixture{path: filepath.Join(t.TempDir(), "cascade.db")}
	f.reg = NewMemoryRegistry()
	f.clock = testkit.NewFrozenClock(baseTime)
	if err := f.reg.Add(context.Background(), readCap()); err != nil {
		t.Fatalf("registering the capability: %v", err)
	}
	f.open(t)
	return f
}

// open (re)opens the database file and rebuilds the store over it. The
// registry and clock are deliberately carried across, so a reopen models a
// daemon restart against the SAME on-disk state rather than a fresh world.
func (f *grantFixture) open(t *testing.T) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), f.path)
	if err != nil {
		t.Fatalf("opening the real SQLite store: %v", err)
	}
	f.db = db
	store, err := NewStoreGrants(db, f.reg, f.clock)
	if err != nil {
		t.Fatalf("NewStoreGrants: %v", err)
	}
	f.store = store
	t.Cleanup(func() { _ = db.Close() })
}

// reopen closes and reopens the database, simulating a daemon restart.
func (f *grantFixture) reopen(t *testing.T) {
	t.Helper()
	if err := f.db.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}
	f.open(t)
}

// validGrant is an unconditional, non-expiring team-class grant.
func validGrant() Grant {
	return Grant{
		Subject:    testSubject(),
		Capability: readCap().Name,
		ScopeClass: corpus.VisibilityTeam,
	}
}

// TestGrantStoreRoundTrip covers Grant/Check/List/Revoke through the
// B-layer abstraction, and the persistence requirement: a grant written
// before a simulated restart is readable after reopen.
func TestGrantStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newGrantFixture(t)

	if err := f.store.Grant(ctx, validGrant()); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	d, err := f.store.Check(ctx, CheckRequest{Subject: testSubject(), Capability: readCap().Name})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !d.Granted {
		t.Fatal("Check returned a nil error with Granted=false")
	}
	if d.Capability.Name != readCap().Name || d.ScopeClass != corpus.VisibilityTeam {
		t.Fatalf("Decision = %+v, want the registered capability at team scope", d)
	}

	list, err := f.store.List(ctx, testSubject())
	if err != nil || len(list) != 1 || list[0].Capability != readCap().Name {
		t.Fatalf("List = %v, %v; want the one written grant", list, err)
	}

	// The persistence assertion: close, reopen, and the grant is still
	// there. Nothing is held in process memory.
	f.reopen(t)
	if _, err := f.store.Check(ctx, CheckRequest{Subject: testSubject(), Capability: readCap().Name}); err != nil {
		t.Fatalf("Check after reopen: %v", err)
	}

	// Storage really is the `policy` domain namespace and not some other.
	if _, err := f.db.Get(ctx, string(storage.DomainPolicy), validGrant().key()); err != nil {
		t.Fatalf("grant row is not in the %q domain namespace: %v", storage.DomainPolicy, err)
	}
}

// TestGrantRevocationIsImmediate asserts hard requirement 4: a revoked
// grant is gone on the very next decision, and stays gone across a
// restart. There is no cache that could outlive the revocation.
func TestGrantRevocationIsImmediate(t *testing.T) {
	ctx := context.Background()
	f := newGrantFixture(t)
	if err := f.store.Grant(ctx, validGrant()); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	// Take a decision BEFORE the revoke, so a cache keyed on a prior
	// allow would have something to serve.
	if _, err := f.store.Check(ctx, CheckRequest{Subject: testSubject(), Capability: readCap().Name}); err != nil {
		t.Fatalf("Check before revoke: %v", err)
	}
	if err := f.store.Revoke(ctx, testSubject(), readCap().Name); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	d, err := f.store.Check(ctx, CheckRequest{Subject: testSubject(), Capability: readCap().Name})
	if err == nil {
		t.Fatal("Check succeeded immediately after Revoke")
	}
	if d.Granted {
		t.Fatal("a denied Check returned Granted=true")
	}
	if !strings.Contains(err.Error(), CodeGrantDenied) {
		t.Fatalf("refusal %q does not carry the %q code", err, CodeGrantDenied)
	}
	f.reopen(t)
	if _, err := f.store.Check(ctx, CheckRequest{Subject: testSubject(), Capability: readCap().Name}); err == nil {
		t.Fatal("a revoked grant came back after a restart")
	}
	list, err := f.store.List(ctx, testSubject())
	if err != nil || len(list) != 0 {
		t.Fatalf("List after revoke = %v, %v; want empty", list, err)
	}
}

// TestGrantRevokeAbsentIsNotSilent asserts that revoking something that is
// not held reports the fact rather than reporting success.
func TestGrantRevokeAbsentIsNotSilent(t *testing.T) {
	ctx := context.Background()
	f := newGrantFixture(t)
	err := f.store.Revoke(ctx, testSubject(), readCap().Name)
	if !errors.Is(err, cascade.ErrCapabilityDenied) {
		t.Fatalf("Revoke(absent) = %v, want grant-denied", err)
	}
	if err := f.store.Revoke(ctx, Subject{}, readCap().Name); err == nil {
		t.Fatal("Revoke accepted an empty subject")
	}
	if err := f.store.Revoke(ctx, testSubject(), "bad name"); err == nil {
		t.Fatal("Revoke accepted a malformed capability name")
	}
}

// TestGrantExpiry asserts expiry is measured against the INJECTED clock,
// that the boundary instant is already expired, and that the refusal names
// the expiry timestamp (the acceptance criterion's wording).
func TestGrantExpiry(t *testing.T) {
	ctx := context.Background()
	f := newGrantFixture(t)
	expires := baseTime.Add(time.Hour)
	g := validGrant()
	g.ExpiresAt = expires
	if err := f.store.Grant(ctx, g); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	if _, err := f.store.Check(ctx, CheckRequest{Subject: testSubject(), Capability: readCap().Name}); err != nil {
		t.Fatalf("Check before expiry: %v", err)
	}

	// The boundary: at exactly ExpiresAt the grant has expired.
	f.clock.Set(expires)
	_, err := f.store.Check(ctx, CheckRequest{Subject: testSubject(), Capability: readCap().Name})
	if err == nil {
		t.Fatal("Check succeeded at exactly the expiry instant")
	}
	if !strings.Contains(err.Error(), expires.Format(time.RFC3339)) {
		t.Fatalf("expiry refusal %q does not name the expiry timestamp", err)
	}

	f.clock.Advance(24 * time.Hour)
	if _, err := f.store.Check(ctx, CheckRequest{Subject: testSubject(), Capability: readCap().Name}); err == nil {
		t.Fatal("Check succeeded well past expiry")
	}
}

// TestGrantWriteRefusesAlreadyExpired asserts a grant that is dead on
// arrival is refused rather than stored as a row that can never be
// honoured.
func TestGrantWriteRefusesAlreadyExpired(t *testing.T) {
	ctx := context.Background()
	f := newGrantFixture(t)
	g := validGrant()
	g.ExpiresAt = baseTime.Add(-time.Second)
	if err := f.store.Grant(ctx, g); err == nil {
		t.Fatal("Grant accepted an already-expired grant")
	}
	if _, err := f.store.Check(ctx, CheckRequest{Subject: testSubject(), Capability: readCap().Name}); err == nil {
		t.Fatal("an already-expired grant was nonetheless honoured")
	}
}

// TestGrantConditionsNarrowOnly asserts conditions can only narrow: an
// absent attribute denies, a differing attribute denies, and an attribute
// the grant does not mention neither grants nor denies anything.
func TestGrantConditionsNarrowOnly(t *testing.T) {
	ctx := context.Background()
	f := newGrantFixture(t)
	g := validGrant()
	g.Conditions = map[string]string{"repo": "cascade"}
	if err := f.store.Grant(ctx, g); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	cases := []struct {
		name  string
		attrs map[string]string
		allow bool
	}{
		{"matching condition allows", map[string]string{"repo": "cascade"}, true},
		{"extra attributes are ignored", map[string]string{"repo": "cascade", "x": "y"}, true},
		{"missing attribute denies", nil, false},
		{"empty attributes deny", map[string]string{}, false},
		{"differing attribute denies", map[string]string{"repo": "other"}, false},
		{"wrong key denies", map[string]string{"repository": "cascade"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := f.store.Check(ctx, CheckRequest{
				Subject: testSubject(), Capability: readCap().Name, Attributes: tc.attrs,
			})
			if tc.allow && err != nil {
				t.Fatalf("Check = %v, want allowed", err)
			}
			if !tc.allow && (err == nil || d.Granted) {
				t.Fatalf("Check = %+v, %v; want denied", d, err)
			}
		})
	}
}

// TestNewStoreGrantsRequiresItsDependencies asserts a store that could not
// evaluate a permission is refused at construction rather than built and
// then failing open at decision time.
func TestNewStoreGrantsRequiresItsDependencies(t *testing.T) {
	var nilStore provider.Store
	reg := NewMemoryRegistry()
	clock := testkit.NewFrozenClock(baseTime)
	if _, err := NewStoreGrants(nilStore, reg, clock); err == nil {
		t.Error("NewStoreGrants accepted a nil store")
	}
	f := newGrantFixture(t)
	if _, err := NewStoreGrants(f.db, nil, clock); err == nil {
		t.Error("NewStoreGrants accepted a nil registry")
	}
	if _, err := NewStoreGrants(f.db, reg, nil); err == nil {
		t.Error("NewStoreGrants accepted a nil clock")
	}
}
