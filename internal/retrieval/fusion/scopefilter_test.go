package fusion

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
)

// scenario is one side of the shared-vocabulary leak fixture that the
// corpus package owns. It is loaded rather than restated so a rename in
// the model breaks the load instead of being papered over here.
type scenario struct {
	Corpus     corpus.Corpus     `json:"corpus"`
	Membership corpus.Membership `json:"membership"`
	Records    []corpus.Record   `json:"records"`
}

func loadScenario(t *testing.T, name string) scenario {
	t.Helper()
	path := filepath.Join("..", "corpus", "testdata", "scope-leak", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	var s scenario
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}
	return s
}

// leakStore builds a store holding both projects, which is the condition
// the leak test needs: both bodies of content are present and reachable in
// principle, so the only thing keeping them apart is the scope decision.
func leakStore(t *testing.T) (*corpus.Store, scenario, scenario) {
	t.Helper()
	p1 := loadScenario(t, "corpus_project1.json")
	p3 := loadScenario(t, "corpus_project3.json")
	store := corpus.NewStore()
	for _, s := range []scenario{p1, p3} {
		if err := store.AddCorpus(s.Corpus); err != nil {
			t.Fatalf("adding corpus %s: %v", s.Corpus.ID, err)
		}
		for _, r := range s.Records {
			if err := store.AddRecord(r); err != nil {
				t.Fatalf("adding record %s: %v", r.ID, err)
			}
		}
	}
	return store, p1, p3
}

// TestScopeFilter_NoLeakAcrossProjects is the shared-vocabulary leak
// assertion, applied to the filter that runs before either retrieval leg.
// The two projects hold near-identical record ids on purpose: if scope
// were dropped or applied after ranking, each side would match the other's
// content and the leak would be invisible.
func TestScopeFilter_NoLeakAcrossProjects(t *testing.T) {
	store, p1, p3 := leakStore(t)

	filter, err := NewScopeFilter(store, corpus.Query{
		Membership:  p1.Membership,
		Entitlement: corpus.PrivacyProject,
	})
	if err != nil {
		t.Fatalf("NewScopeFilter: %v", err)
	}

	for _, r := range filter.Candidates() {
		if r.CorpusID == p3.Corpus.ID {
			t.Errorf("a Project 1 session reached %s, from another project's corpus", r.ID)
		}
	}
	if len(filter.Candidates()) != len(p1.Records) {
		t.Errorf("candidates = %d, want Project 1's own %d records",
			len(filter.Candidates()), len(p1.Records))
	}
	for _, r := range p3.Records {
		if _, ok := filter.Resolve(r.ID); ok {
			t.Errorf("Resolve admitted %s, a record from another project", r.ID)
		}
	}
	foreign := NamespaceFor(p3.Corpus.ID)
	for _, ns := range filter.Namespaces() {
		if ns == foreign {
			t.Errorf("namespaces include %s, which holds another project's vectors", ns)
		}
	}
	for _, id := range filter.Predicate().CorpusIDs {
		if id == p3.Corpus.ID {
			t.Errorf("the full-text predicate names %s, another project's corpus", id)
		}
	}
}

// TestScopeFilter_DeclaredEdgeReaches is the control. Without it the leak
// test could pass because the filter admits nothing at all.
func TestScopeFilter_DeclaredEdgeReaches(t *testing.T) {
	store, p1, p3 := leakStore(t)

	membership := p1.Membership
	membership.Edges = append(membership.Edges, corpus.Edge{
		Kind:   corpus.EdgeSharesContextWith,
		Target: p3.Corpus.ScopeRef,
	})
	filter, err := NewScopeFilter(store, corpus.Query{
		Membership:  membership,
		Entitlement: corpus.PrivacyProject,
	})
	if err != nil {
		t.Fatalf("NewScopeFilter: %v", err)
	}
	for _, r := range p3.Records {
		if _, ok := filter.Resolve(r.ID); !ok {
			t.Errorf("%s did not reach across a declared edge", r.ID)
		}
	}
	want := NamespaceFor(p3.Corpus.ID)
	var found bool
	for _, ns := range filter.Namespaces() {
		if ns == want {
			found = true
		}
	}
	if !found {
		t.Errorf("namespaces = %v, want the edge target's namespace among them", filter.Namespaces())
	}
}

// TestScopeFilter_TrustTagSurvives checks the tag reaching the filter's
// output intact, including the untrusted record the fixture places inside
// an otherwise trusted corpus.
func TestScopeFilter_TrustTagSurvives(t *testing.T) {
	store, p1, _ := leakStore(t)
	filter, err := NewScopeFilter(store, corpus.Query{
		Membership:  p1.Membership,
		Entitlement: corpus.PrivacyProject,
	})
	if err != nil {
		t.Fatalf("NewScopeFilter: %v", err)
	}
	var untrusted int
	for _, want := range p1.Records {
		got, ok := filter.Resolve(want.ID)
		if !ok {
			t.Fatalf("%s was withheld from its own session", want.ID)
		}
		if got.Trust != want.Trust {
			t.Errorf("%s carries trust %q, the fixture declares %q", want.ID, got.Trust, want.Trust)
		}
		if got.Trust == corpus.TrustUntrustedSource {
			untrusted++
		}
	}
	if untrusted == 0 {
		t.Error("no untrusted record in the candidate set, so this asserts nothing about propagation")
	}
}

func TestScopeFilter_ErrorPaths(t *testing.T) {
	store, p1, _ := leakStore(t)

	if _, err := NewScopeFilter(nil, corpus.Query{Membership: p1.Membership}); err == nil {
		t.Error("a nil store was accepted")
	} else if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("nil store error kind = %v (taxonomy %t), want invalid input", kind, ok)
	}

	// A malformed membership is refused rather than silently matching
	// nothing, so a caller with a broken membership is told about it
	// instead of concluding the index is empty.
	if _, err := NewScopeFilter(store, corpus.Query{
		Membership: corpus.Membership{Scope: ""}, Entitlement: corpus.PrivacyProject,
	}); err == nil {
		t.Error("a membership with no scope was accepted")
	}
}

// TestScopeFilter_EmptyMembershipAdmitsNothing pins deny-by-default at
// this boundary too.
func TestScopeFilter_EmptyMembershipAdmitsNothing(t *testing.T) {
	store, _, _ := leakStore(t)
	filter, err := NewScopeFilter(store, corpus.Query{
		Membership:  corpus.Membership{Scope: "project:unrelated"},
		Entitlement: corpus.PrivacyProject,
	})
	if err != nil {
		t.Fatalf("NewScopeFilter: %v", err)
	}
	if !filter.Empty() {
		t.Errorf("an unrelated scope reached %d records", len(filter.Candidates()))
	}
	if len(filter.Namespaces()) != 0 {
		t.Errorf("an unrelated scope reached namespaces %v", filter.Namespaces())
	}
}

func TestNamespaceForIsPerCorpus(t *testing.T) {
	if NamespaceFor("a") == NamespaceFor("b") {
		t.Error("two corpora share a namespace, which would put one scope's vectors in another's reach")
	}
	if !strings.HasPrefix(NamespaceFor("a"), namespacePrefix) {
		t.Errorf("namespace %q does not carry the retrieval prefix", NamespaceFor("a"))
	}
}

// TestNoPostRankScopeFiltering asserts the structural rule: scope is
// enforced in one place, before the legs run, and nowhere after ranking.
// This is a source assertion because the property is about where code is
// allowed to exist, which no behavioural test can observe.
func TestNoPostRankScopeFiltering(t *testing.T) {
	if _, err := os.Stat(filepath.Join("..", "rrf", "scope.go")); err == nil {
		t.Error("internal/retrieval/rrf/scope.go exists. There is exactly one scope-enforcement " +
			"mechanism, this package's ScopeFilter, and a second one in the ranking core would be " +
			"a scope check applied after ranking")
	}

	// The ranking core must not be able to make a scope decision at all:
	// it is handed an already-narrowed candidate set and has no access to
	// the model that would let it narrow one.
	forbidden := []string{"ScopeFilter", "Membership", "corpus.Store", "corpus.Query"}
	entries, err := os.ReadDir(filepath.Join("..", "rrf"))
	if err != nil {
		t.Fatalf("reading the ranking core: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join("..", "rrf", name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		for _, f := range forbidden {
			if strings.Contains(string(body), f) {
				t.Errorf("rrf/%s references %s: the ranking core must not be able to decide scope", name, f)
			}
		}
	}
}

// TestScopeBindingRunsBeforeRanking checks the vector leg's own ordering:
// the candidate resolution runs on the driver's raw response, before the
// merged list is ever sorted.
func TestScopeBindingRunsBeforeRanking(t *testing.T) {
	body, err := os.ReadFile("vectorleg.go")
	if err != nil {
		t.Fatalf("reading vectorleg.go: %v", err)
	}
	src := string(body)
	resolve := strings.Index(src, "resolveMatches(filter, matches)")
	rank := strings.Index(src, "sort.Slice(hits")
	if resolve < 0 || rank < 0 {
		t.Fatal("the vector leg no longer resolves candidates and ranks them in the shape this test reads")
	}
	if resolve > rank {
		t.Error("candidate resolution happens after the ranking sort, which makes it post-rank filtering")
	}
}
