package corpus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestScopeRef_Valid(t *testing.T) {
	valid := []ScopeRef{"project:one", "user:local", "workspace:w/sub", "a"}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("%q should be a valid scope reference", string(s))
		}
	}
	invalid := []ScopeRef{
		"",
		"project one",
		"project:one\n",
		"\tproject:one",
		"project: one",
		ScopeRef(strings.Repeat("x", maxScopeRefLen+1)),
	}
	for _, s := range invalid {
		if s.Valid() {
			t.Errorf("%q should not be a valid scope reference", string(s))
		}
	}
	if !ScopeRef(strings.Repeat("x", maxScopeRefLen)).Valid() {
		t.Error("a reference exactly at the length bound should be valid")
	}
}

// TestEdgeKind_SpelledAsTheScopeModelDeclaresThem pins the three kinds as
// literals. These spellings are the persisted form of a relationship that
// widens a candidate set, so a drifted spelling would either silently stop
// widening or silently start.
func TestEdgeKind_SpelledAsTheScopeModelDeclaresThem(t *testing.T) {
	want := map[EdgeKind]string{
		EdgeDependsOn:         "depends_on",
		EdgeMemberOf:          "member_of",
		EdgeSharesContextWith: "shares_context_with",
	}
	for kind, spelling := range want {
		if string(kind) != spelling {
			t.Errorf("edge kind stored as %q, want %q", string(kind), spelling)
		}
		if !kind.Valid() {
			t.Errorf("%q should be a valid edge kind", spelling)
		}
	}
	for _, bad := range []EdgeKind{"", "dependsOn", "depends-on", "parent_of", "related"} {
		if bad.Valid() {
			t.Errorf("%q should not be a valid edge kind", string(bad))
		}
	}
}

func TestEdge_Validate(t *testing.T) {
	if err := (Edge{Kind: EdgeMemberOf, Target: "workspace:w"}).Validate(); err != nil {
		t.Fatalf("a well-formed edge should validate: %v", err)
	}
	for _, bad := range []Edge{
		{Kind: "related", Target: "workspace:w"},
		{Kind: "", Target: "workspace:w"},
		{Kind: EdgeMemberOf, Target: ""},
		{Kind: EdgeMemberOf, Target: "bad scope"},
	} {
		err := (bad).Validate()
		if err == nil {
			t.Fatalf("edge %+v should not validate", bad)
		}
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("edge %+v: kind wrong, err = %v, want invalid input", bad, err)
		}
	}
}

func TestMembership_Validate(t *testing.T) {
	good := Membership{
		Scope: "project:one",
		Chain: []ScopeRef{"user:local", "project:one"},
		Edges: []Edge{{Kind: EdgeDependsOn, Target: "project:two"}},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("a well-formed membership should validate: %v", err)
	}
	bad := []Membership{
		{},
		{Scope: "bad scope"},
		{Scope: "project:one", Chain: []ScopeRef{""}},
		{Scope: "project:one", Chain: []ScopeRef{"user local"}},
		{Scope: "project:one", Edges: []Edge{{Kind: "related", Target: "project:two"}}},
	}
	for _, m := range bad {
		if err := m.Validate(); err == nil {
			t.Errorf("membership %+v should not validate", m)
		} else if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("membership %+v: kind wrong, err = %v, want invalid input", m, err)
		}
	}
}

// TestMembership_InChain_DeniesByDefault covers the empty membership, the
// invalid reference and the case an equality-only implementation would get
// wrong: two empty references comparing equal.
func TestMembership_InChain_DeniesByDefault(t *testing.T) {
	empty := Membership{}
	if empty.inChain("") {
		t.Error("an empty membership must not admit an empty reference")
	}
	if empty.inChain("project:one") {
		t.Error("an empty membership must admit nothing")
	}
	m := Membership{Scope: "project:one", Chain: []ScopeRef{"user:local"}}
	if !m.inChain("project:one") || !m.inChain("user:local") {
		t.Error("the own scope and every chain entry must be in the chain")
	}
	if m.inChain("project:two") || m.inChain("") || m.inChain("bad scope") {
		t.Error("nothing outside the chain may be in it")
	}
}

// TestMembership_AcrossEdge_SkipsMalformedEdges proves a malformed edge is
// never the thing that admits a foreign scope: the edge is skipped, not
// honored, so a corrupt row cannot widen the candidate set.
func TestMembership_AcrossEdge_SkipsMalformedEdges(t *testing.T) {
	m := Membership{
		Scope: "project:one",
		Edges: []Edge{
			{Kind: "related", Target: "project:sneaky"},
			{Kind: "", Target: "project:alsoSneaky"},
			{Kind: EdgeSharesContextWith, Target: "project:declared"},
		},
	}
	if !m.acrossEdge("project:declared") {
		t.Error("a declared, valid edge target must be reachable")
	}
	for _, ref := range []ScopeRef{"project:sneaky", "project:alsoSneaky", "project:unrelated", ""} {
		if m.acrossEdge(ref) {
			t.Errorf("%q must not be reachable across an edge", string(ref))
		}
	}
}

func TestDescribeScope(t *testing.T) {
	if got := describeScope(""); got != "(empty)" {
		t.Errorf("describeScope(empty) = %q", got)
	}
	if got := describeScope("project:one"); got != "project:one" {
		t.Errorf("describeScope = %q", got)
	}
	if got := describeScope("proj\x00ect\r\n"); got != "project" {
		t.Errorf("describeScope must strip control characters, got %q", got)
	}
	long := describeScope(ScopeRef(strings.Repeat("y", 200)))
	if len(long) > 70 || !strings.HasSuffix(long, "...") {
		t.Errorf("describeScope must bound its output, got %d chars", len(long))
	}
}

// scopeLeakFixture is one side of the shared-vocabulary leak scenario as
// stored under testdata/scope-leak (see that directory's README for
// provenance). The field names are the model's own JSON tags, so a rename
// in the model breaks the load rather than silently loading zero values.
type scopeLeakFixture struct {
	Corpus     Corpus     `json:"corpus"`
	Membership Membership `json:"membership"`
	Records    []Record   `json:"records"`
}

func loadScopeLeakFixture(t *testing.T, name string) scopeLeakFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "scope-leak", name))
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

// storeFromScopeLeakFixtures builds a Store holding both projects. Loading
// through AddCorpus/AddRecord means the fixture is also a validation test:
// a fixture row with an unclassified or malformed field fails here rather
// than quietly entering the store.
func storeFromScopeLeakFixtures(t *testing.T) (*Store, scopeLeakFixture, scopeLeakFixture) {
	t.Helper()
	one := loadScopeLeakFixture(t, "corpus_project1.json")
	three := loadScopeLeakFixture(t, "corpus_project3.json")
	s := NewStore()
	for _, f := range []scopeLeakFixture{one, three} {
		if err := s.AddCorpus(f.Corpus); err != nil {
			t.Fatalf("adding corpus %s: %v", f.Corpus.ID, err)
		}
		for _, r := range f.Records {
			if err := s.AddRecord(r); err != nil {
				t.Fatalf("adding record %s: %v", r.ID, err)
			}
		}
	}
	return s, one, three
}

// TestScopeLeak_NoRecordCrossesWithoutADeclaredEdge is the leak assertion
// the fixture exists for. The two projects share record vocabulary, so
// content alone would match across the boundary; only membership keeps
// them apart.
func TestScopeLeak_NoRecordCrossesWithoutADeclaredEdge(t *testing.T) {
	s, one, three := storeFromScopeLeakFixtures(t)

	got, err := s.Query(Query{Membership: one.Membership, Entitlement: PrivacyProject})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != len(one.Records) {
		t.Fatalf("a project 1 session saw %d records, want its own %d", len(got), len(one.Records))
	}
	for _, r := range got {
		if r.CorpusID != one.Corpus.ID {
			t.Fatalf("leak: record %s from corpus %s surfaced for a project 1 session", r.ID, r.CorpusID)
		}
	}

	// The far side is classed shared, the widest reach core grants, so the
	// absent edge is the only control under test.
	if three.Corpus.Visibility != VisibilityShared {
		t.Fatalf("fixture project 3 corpus is %q, the leak test needs it shared", three.Corpus.Visibility.String())
	}
	if len(one.Membership.Edges) != 0 {
		t.Fatalf("fixture project 1 membership declares %d edges, the leak test needs none", len(one.Membership.Edges))
	}
}

// TestScopeLeak_ADeclaredEdgeIsTheOnlyThingThatOpensIt proves the previous
// test is not passing because the query returns nothing regardless. Adding
// the one declared edge, and nothing else, surfaces the far side's records.
func TestScopeLeak_ADeclaredEdgeIsTheOnlyThingThatOpensIt(t *testing.T) {
	s, one, three := storeFromScopeLeakFixtures(t)

	linked := one.Membership
	linked.Edges = []Edge{{Kind: EdgeSharesContextWith, Target: three.Corpus.ScopeRef}}
	got, err := s.Query(Query{Membership: linked, Entitlement: PrivacyProject})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	var fromThree int
	for _, r := range got {
		if r.CorpusID == three.Corpus.ID {
			fromThree++
		}
	}
	if fromThree != len(three.Records) {
		t.Fatalf("with a declared edge a project 1 session saw %d of project 3's %d records",
			fromThree, len(three.Records))
	}
}

// TestStore_Query_CorpusIDsOnlyNarrow proves naming a corpus is never a
// grant: an unknown id matches nothing, and a named id cannot pull in a
// record the membership would otherwise deny.
func TestStore_Query_CorpusIDsOnlyNarrow(t *testing.T) {
	s := seededStore(t)
	got, err := s.Query(Query{Membership: ownMembership(), Entitlement: PrivacyProject, CorpusIDs: []string{"docs"}})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("naming the record's own corpus returned %d records, want 2", len(got))
	}
	got, err = s.Query(Query{Membership: ownMembership(), Entitlement: PrivacyProject, CorpusIDs: []string{"no-such-corpus"}})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("naming an unknown corpus returned %d records, want none", len(got))
	}
}
