// Purpose: R-14.5 domain-layout tests — proves AllDomains carries exactly
//
//	the eleven ratified domains (R-14.5's ten as amended by R-16.51,
//	which adds `policy`), in a fixed order, and that the exhaustive
//	switch every real DomainID consumer must write actually recognizes
//	all eleven. Split from domains_test.go as a sibling file per R-14.117
//	(Art.10.3 300-line cap; mechanical relocation, no behavior change).
//
// SPORT: internal.storage.domains.AllDomains/ADDED,
//
//	internal.storage.domains.Bootstrap/ADDED (P1-E02-W1-S03-T1).
package storage_test

import (
	"testing"

	"github.com/acamarata/cascade/internal/storage"
)

// TestDomainLayout_AllDomainsDeterministic asserts AllDomains carries
// exactly the eleven ratified domains (R-14.5's ten plus R-16.51's
// `policy`), in a fixed, repeatable order, and that
// every switch consumer of DomainID (domainLayoutExhaustiveSwitch below)
// compiles exhaustively — golangci-lint's `exhaustive` analyzer (R-14.101)
// is the enforcement layer that catches a forgotten case at lint time; this
// test proves the same eleven values at run time.
func TestDomainLayout_AllDomainsDeterministic(t *testing.T) {
	want := []storage.DomainID{
		storage.DomainContext, storage.DomainMemory, storage.DomainAudit,
		storage.DomainSecrets, storage.DomainSessions, storage.DomainConfig,
		storage.DomainRetrieval, storage.DomainBlobs, storage.DomainQueue,
		storage.DomainJobs, storage.DomainPolicy,
	}
	if len(storage.AllDomains) != len(want) {
		t.Fatalf("AllDomains has %d entries, want %d", len(storage.AllDomains), len(want))
	}
	for i, meta := range storage.AllDomains {
		if meta.ID != want[i] {
			t.Errorf("AllDomains[%d].ID = %q, want %q (order must be deterministic)", i, meta.ID, want[i])
		}
		if meta.TablePrefix == "" {
			t.Errorf("AllDomains[%d] (%s): empty TablePrefix", i, meta.ID)
		}
		if meta.OwnerPkg == "" {
			t.Errorf("AllDomains[%d] (%s): empty OwnerPkg", i, meta.ID)
		}
		if !domainLayoutExhaustiveSwitch(meta.ID) {
			t.Errorf("AllDomains[%d].ID = %q not recognized by the exhaustive switch (closed set violated)", i, meta.ID)
		}
	}

	// Repeated calls (Go const/var initialization is deterministic, but
	// this pins the invariant as a test rather than an assumption) return
	// the identical order.
	for i, meta := range storage.AllDomains {
		if meta.ID != want[i] {
			t.Fatalf("AllDomains order changed on re-read at index %d", i)
		}
	}
}

// domainLayoutExhaustiveSwitch mirrors the closed-set switch every real
// consumer (Bootstrap, StorageHealthCheck) must write: golangci-lint's
// `exhaustive` analyzer fails the build if a case is missing here, which
// is the point — a forgotten R-14.5 domain becomes a lint failure, not a
// silent gap. Returns true for every one of the eleven recognized IDs.
func domainLayoutExhaustiveSwitch(id storage.DomainID) bool {
	switch id {
	case storage.DomainContext, storage.DomainMemory, storage.DomainAudit,
		storage.DomainSecrets, storage.DomainSessions, storage.DomainConfig,
		storage.DomainRetrieval, storage.DomainBlobs, storage.DomainQueue,
		storage.DomainJobs, storage.DomainPolicy:
		return true
	}
	return false
}
