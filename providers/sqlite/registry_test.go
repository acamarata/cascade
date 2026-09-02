// Purpose: DomainRegistry conformance — a second owner is refused, release
//
//	frees the domain for a subsequent owner, and independent domains never
//	contend with each other. Split from driver_test.go under R-14.117.
//
// SPORT: providers.sqlite.DomainRegistry/ADDED (P1-E02-W1-S02-T2).
package sqlite_test

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/providers/sqlite"
)

// TestDriver_OwnDomain_RefusesSecondOwner proves the domain-ownership
// registry: a second OwnDomain of the same domain while the first is held
// is refused with ErrDomainOwned, and releasing the first lets a third
// caller succeed.
func TestDriver_OwnDomain_RefusesSecondOwner(t *testing.T) {
	d := newTestDriver(t)

	release1, err := d.OwnDomain("context")
	if err != nil {
		t.Fatalf("first OwnDomain: %v", err)
	}

	_, err = d.OwnDomain("context")
	if !errors.Is(err, sqlite.ErrDomainOwned) {
		t.Fatalf("second OwnDomain: want errors.Is(err, ErrDomainOwned), got %v", err)
	}

	release1()

	release2, err := d.OwnDomain("context")
	if err != nil {
		t.Fatalf("OwnDomain after release: %v", err)
	}
	release2()
}

// TestDriver_OwnDomain_IndependentDomains proves two different domains
// never contend with each other.
func TestDriver_OwnDomain_IndependentDomains(t *testing.T) {
	d := newTestDriver(t)

	releaseA, err := d.OwnDomain("memory")
	if err != nil {
		t.Fatalf("OwnDomain(memory): %v", err)
	}
	defer releaseA()

	releaseB, err := d.OwnDomain("audit")
	if err != nil {
		t.Fatalf("OwnDomain(audit): %v", err)
	}
	defer releaseB()
}
