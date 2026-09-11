package corpus

// Purpose: the R-21.190 graph-leg extension of the R-16.4 shared-
//   vocabulary scope-leak fixture (testdata/scope-leak/graph-leg/):
//   proves a `graph`-shaped corpus record never crosses an un-declared
//   scope boundary through the EXISTING Store.Query/Membership path --
//   no new corpus mechanism, per HOW-6's "no fusion-code changes".
// SPORT: retrieval/graph-corpus-type/ADD (P1-E33-W7-S67-T3).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadGraphLegFixture reuses scopeLeakFixture (scope_test.go, this same
// package) against the graph-leg subdirectory rather than duplicating
// its shape.
func loadGraphLegFixture(t *testing.T, name string) scopeLeakFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "scope-leak", "graph-leg", name))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	var f scopeLeakFixture
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("decoding fixture %s: %v", name, err)
	}
	if len(f.Records) == 0 {
		t.Fatalf("fixture %s carries no records, so it would prove nothing", name)
	}
	return f
}

func storeFromGraphLegFixtures(t *testing.T) (*Store, scopeLeakFixture, scopeLeakFixture) {
	t.Helper()
	a := loadGraphLegFixture(t, "scope-a.json")
	b := loadGraphLegFixture(t, "scope-b.json")
	s := NewStore()
	for _, f := range []scopeLeakFixture{a, b} {
		if err := s.AddCorpus(f.Corpus); err != nil {
			t.Fatalf("adding corpus %s: %v", f.Corpus.ID, err)
		}
		for _, r := range f.Records {
			if err := s.AddRecord(r); err != nil {
				t.Fatalf("adding record %s: %v", r.ID, err)
			}
		}
	}
	return s, a, b
}

// TestGraphLegNoScopeLeak proves a graph query in scope A returns zero
// records, and zero symbol names originating in scope B, when no
// permitting scope edge exists.
func TestGraphLegNoScopeLeak(t *testing.T) {
	s, a, b := storeFromGraphLegFixtures(t)

	got, err := s.Query(Query{Membership: a.Membership, Entitlement: PrivacyProject})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != len(a.Records) {
		t.Fatalf("a scope-a session saw %d records, want its own %d", len(got), len(a.Records))
	}
	for _, r := range got {
		if r.CorpusID == b.Corpus.ID {
			t.Fatalf("leak: graph record %s from scope-b surfaced for a scope-a session with no permitting edge", r.ID)
		}
	}
	if b.Corpus.Visibility != VisibilityShared {
		t.Fatalf("fixture scope-b corpus is %q, the leak test needs it shared", b.Corpus.Visibility.String())
	}
	if len(a.Membership.Edges) != 0 {
		t.Fatalf("fixture scope-a membership declares %d edges, the leak test needs none", len(a.Membership.Edges))
	}
}

// TestGraphLegDeclaredEdgeOpensIt proves the previous test is not
// passing because the query returns nothing regardless: adding one
// declared depends_on edge, and nothing else, surfaces scope-b's graph
// records (and the symbol names they carry) to a scope-a session.
func TestGraphLegDeclaredEdgeOpensIt(t *testing.T) {
	s, a, b := storeFromGraphLegFixtures(t)

	linked := a.Membership
	linked.Edges = []Edge{{Kind: EdgeDependsOn, Target: b.Corpus.ScopeRef}}
	got, err := s.Query(Query{Membership: linked, Entitlement: PrivacyProject})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	var fromB int
	for _, r := range got {
		if r.CorpusID == b.Corpus.ID {
			fromB++
		}
	}
	if fromB != len(b.Records) {
		t.Fatalf("with a declared depends_on edge a scope-a session saw %d of scope-b's %d graph records", fromB, len(b.Records))
	}
}
