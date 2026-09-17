package sync

// Purpose (this file): which strategy each domain gets, and the rule that
//   an unmapped domain gets none.
// SPORT: internal/sync tests (ADD) — P1-E17-W4-S38-T2.

import (
	"testing"

	"github.com/acamarata/cascade/internal/storage"
)

// TestEveryRegisteredDomainHasItsDecidedStrategy enumerates the registry
// key by key. A table-driven check over AllCoreClasses would pass against
// an implementation that returned the same strategy for everything.
func TestEveryRegisteredDomainHasItsDecidedStrategy(t *testing.T) {
	for _, tc := range []struct {
		domain  storage.DomainID
		subkind string
		want    StrategyName
	}{
		{storage.DomainConfig, "config", StrategyServerPrimaryLWW},
		{storage.DomainConfig, "registry", StrategyMetadataOnly},
		{storage.DomainConfig, "accounts", StrategyMetadataOnly},
		{storage.DomainConfig, "phase-state", StrategyGitCarried},
		{storage.DomainMemory, "memory", StrategyAppendTombstone},
		{storage.DomainContext, "conversation", StrategyAppendTombstone},
		{storage.DomainBlobs, "blobs", StrategyContentAddressUnion},
	} {
		t.Run(string(tc.domain)+"/"+tc.subkind, func(t *testing.T) {
			got, mapped := StrategyFor(tc.domain, tc.subkind)
			if !mapped {
				t.Fatalf("%s/%s is not mapped at all", tc.domain, tc.subkind)
			}
			if got != tc.want {
				t.Errorf("strategy = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEveryRegisteredDomainIsCoveredHere guards against the table above
// going stale: a domain added to the registry with no line here would
// otherwise be untested, and its strategy would be whatever the switch
// happened to fall through to.
func TestEveryRegisteredDomainIsCoveredHere(t *testing.T) {
	covered := map[string]bool{
		"config": true, "registry": true, "accounts": true, "phase-state": true,
		"memory": true, "conversation": true, "blobs": true,
	}
	for _, dc := range AllCoreClasses() {
		if !covered[dc.Subkind] {
			t.Errorf("the registry carries %q, which TestEveryRegisteredDomainHasItsDecidedStrategy "+
				"does not name — its strategy is whatever the switch fell through to", dc.Subkind)
		}
	}
}

// TestAnUnmappedDomainNeverSyncs is the fail-closed rule (R-16.56). It
// returns no-sync rather than an error on purpose: an error would make a
// new domain break sync until somebody mapped it, and the pressure would
// be to add a permissive default.
func TestAnUnmappedDomainNeverSyncs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		domain  storage.DomainID
		subkind string
	}{
		{"unregistered domain", storage.DomainAudit, "audit"},
		{"unregistered subkind", storage.DomainConfig, "invented"},
		{"empty subkind", storage.DomainConfig, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, mapped := StrategyFor(tc.domain, tc.subkind)
			if mapped {
				t.Fatalf("%s/%s reported itself mapped", tc.domain, tc.subkind)
			}
			if got != StrategyNone {
				t.Errorf("strategy = %q, want %q — never a default merge", got, StrategyNone)
			}
		})
	}
}

// TestALocalOnlyClassGetsNoStrategy covers the class that is registered
// and still never merges.
func TestALocalOnlyClassGetsNoStrategy(t *testing.T) {
	if got := strategyForClass(DomainClass{
		Domain: storage.DomainConfig, Subkind: "something", Class: ClassLocalOnly,
	}); got != StrategyNone {
		t.Errorf("a local-only class resolved to %q", got)
	}
}

// TestAnUnknownClassFailsClosed proves a fifth class added without
// visiting the switch gets no merge rather than an arbitrary one.
func TestAnUnknownClassFailsClosed(t *testing.T) {
	if got := strategyForClass(DomainClass{
		Domain: storage.DomainConfig, Subkind: "something", Class: Class("invented"),
	}); got != StrategyNone {
		t.Errorf("an unrecognised class resolved to %q, want %q", got, StrategyNone)
	}
}
