package corpus

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func validCorpus() Corpus {
	return Corpus{
		ID:         "docs",
		ScopeRef:   "project:one",
		Privacy:    PrivacyProject,
		Visibility: VisibilityScopeLocal,
		Trust:      TrustTrusted,
	}
}

func validRecord() Record {
	return Record{
		ID:         "docs/readme.md#1",
		CorpusID:   "docs",
		ScopeRef:   "project:one",
		Privacy:    PrivacyProject,
		Visibility: VisibilityScopeLocal,
		Trust:      TrustTrusted,
	}
}

func ownMembership() Membership {
	return Membership{Scope: "project:one", Chain: []ScopeRef{"user:local", "project:one"}}
}

// TestCorpus_Validate_RefusesEveryUnclassifiedField proves there is no
// default on any axis: an omitted or unrecognized enum is refused, never
// coerced into a value the author did not write.
func TestCorpus_Validate_RefusesEveryUnclassifiedField(t *testing.T) {
	if err := validCorpus().Validate(); err != nil {
		t.Fatalf("a fully classified corpus should validate: %v", err)
	}
	mutate := map[string]func(*Corpus){
		"no id":              func(c *Corpus) { c.ID = "" },
		"no scope":           func(c *Corpus) { c.ScopeRef = "" },
		"malformed scope":    func(c *Corpus) { c.ScopeRef = "project one" },
		"no privacy":         func(c *Corpus) { c.Privacy = "" },
		"unknown privacy":    func(c *Corpus) { c.Privacy = "secret" },
		"no visibility":      func(c *Corpus) { c.Visibility = "" },
		"unknown visibility": func(c *Corpus) { c.Visibility = "public" },
		"no trust":           func(c *Corpus) { c.Trust = "" },
		"unknown trust":      func(c *Corpus) { c.Trust = "probably-fine" },
	}
	for name, mut := range mutate {
		t.Run(name, func(t *testing.T) {
			c := validCorpus()
			mut(&c)
			err := c.Validate()
			if err == nil {
				t.Fatal("should not validate")
			}
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Errorf("kind wrong, err = %v, want invalid input", err)
			}
		})
	}
}

func TestRecord_Validate_RefusesEveryUnclassifiedField(t *testing.T) {
	if err := validRecord().Validate(); err != nil {
		t.Fatalf("a fully classified record should validate: %v", err)
	}
	mutate := map[string]func(*Record){
		"no id":              func(r *Record) { r.ID = "" },
		"no corpus id":       func(r *Record) { r.CorpusID = "" },
		"no scope":           func(r *Record) { r.ScopeRef = "" },
		"malformed scope":    func(r *Record) { r.ScopeRef = "project\tone" },
		"no privacy":         func(r *Record) { r.Privacy = "" },
		"unknown privacy":    func(r *Record) { r.Privacy = "secret" },
		"no visibility":      func(r *Record) { r.Visibility = "" },
		"unknown visibility": func(r *Record) { r.Visibility = "public" },
		"no trust":           func(r *Record) { r.Trust = "" },
		"unknown trust":      func(r *Record) { r.Trust = "probably-fine" },
	}
	for name, mut := range mutate {
		t.Run(name, func(t *testing.T) {
			r := validRecord()
			mut(&r)
			err := r.Validate()
			if err == nil {
				t.Fatal("should not validate")
			}
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Errorf("kind wrong, err = %v, want invalid input", err)
			}
		})
	}
}

func TestStore_AddCorpus_RejectsInvalidAndDuplicate(t *testing.T) {
	s := NewStore()
	if err := s.AddCorpus(Corpus{ID: "x"}); err == nil {
		t.Fatal("an unclassified corpus should be refused")
	}
	if err := s.AddCorpus(validCorpus()); err != nil {
		t.Fatalf("adding a valid corpus: %v", err)
	}
	err := s.AddCorpus(validCorpus())
	if err == nil {
		t.Fatal("a duplicate corpus id should conflict rather than overwrite")
	}
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Errorf("kind wrong, err = %v, want conflict", err)
	}
}

func TestStore_AddRecord_RequiresItsCorpus(t *testing.T) {
	s := NewStore()
	err := s.AddRecord(validRecord())
	if err == nil {
		t.Fatal("a record whose corpus is absent should be refused")
	}
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("kind wrong, err = %v, want not found", err)
	}
	if err := s.AddCorpus(validCorpus()); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRecord(validRecord()); err != nil {
		t.Fatalf("adding a valid record: %v", err)
	}
	bad := validRecord()
	bad.Trust = ""
	if err := s.AddRecord(bad); err == nil {
		t.Fatal("an unclassified record should be refused")
	}
}

// TestStore_Query_EmptyStoreAuthorizesNothing covers the empty-corpus case:
// nothing indexed means nothing authorized, and a well-formed query gets an
// empty answer rather than an error or a wildcard.
func TestStore_Query_EmptyStoreAuthorizesNothing(t *testing.T) {
	got, err := NewStore().Query(Query{Membership: ownMembership(), Entitlement: PrivacyProject})
	if err != nil {
		t.Fatalf("query on an empty store: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("an empty store returned %d records", len(got))
	}
}

// TestStore_Query_RefusesAMalformedMembership proves a broken membership is
// reported rather than silently matching nothing, which would look to a
// caller exactly like an empty index.
func TestStore_Query_RefusesAMalformedMembership(t *testing.T) {
	s := seededStore(t)
	for _, m := range []Membership{
		{},
		{Scope: "project one"},
		{Scope: "project:one", Edges: []Edge{{Kind: "related", Target: "project:two"}}},
	} {
		got, err := s.Query(Query{Membership: m, Entitlement: PrivacyProject})
		if err == nil {
			t.Fatalf("membership %+v should be refused", m)
		}
		if got != nil {
			t.Errorf("a refused query must return no records, got %d", len(got))
		}
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("kind wrong, err = %v, want invalid input", err)
		}
	}
}

// corruptStore holds records the write path would have refused. They can
// only arrive from a store written by another version or another process,
// and the read path has to decide what to do with them without the benefit
// of validation.
func corruptStore(t *testing.T) *Store {
	t.Helper()
	s := NewStore()
	if err := s.AddCorpus(validCorpus()); err != nil {
		t.Fatal(err)
	}
	s.records = append(s.records, // bypasses AddRecord on purpose: this is the read-path guard
		Record{ID: "no-scope", CorpusID: "docs", Privacy: PrivacyProject, Visibility: VisibilityScopeLocal, Trust: TrustTrusted},
		Record{ID: "orphan-corpus", CorpusID: "gone", ScopeRef: "project:one", Privacy: PrivacyProject, Visibility: VisibilityScopeLocal, Trust: TrustTrusted},
		Record{ID: "foreign-scope", CorpusID: "docs", ScopeRef: "project:other", Privacy: PrivacyProject, Visibility: VisibilityShared, Trust: TrustTrusted},
		Record{ID: "no-visibility", CorpusID: "docs", ScopeRef: "project:one", Privacy: PrivacyProject, Trust: TrustTrusted},
		Record{ID: "unknown-visibility", CorpusID: "docs", ScopeRef: "project:one", Privacy: PrivacyProject, Visibility: "public", Trust: TrustTrusted},
		Record{ID: "no-privacy", CorpusID: "docs", ScopeRef: "project:one", Visibility: VisibilityScopeLocal, Trust: TrustTrusted},
		Record{ID: "zero-value", CorpusID: "docs"},
		Record{ID: "unknown-trust", CorpusID: "docs", ScopeRef: "project:one", Privacy: PrivacyProject, Visibility: VisibilityPrivate, Trust: "who-knows"},
	)
	return s
}

// TestStore_Query_WithholdsUnresolvableRecordsFromEveryOtherSession is the
// read-path fail-closed guard. A neighbouring session in the same chain
// holds a legitimate membership and a project entitlement, which is the
// widest position an ordinary caller occupies. Not one unvalidated record
// reaches it: an unreadable scope reaches nothing, an unreadable
// visibility collapses to private and so stops at the owning scope, and an
// unreadable privacy flag collapses to personal and so needs an
// entitlement this session does not have.
func TestStore_Query_WithholdsUnresolvableRecordsFromEveryOtherSession(t *testing.T) {
	neighbour := Membership{
		Scope: "project:neighbour",
		Chain: []ScopeRef{"user:local", "project:one", "project:neighbour"},
		Edges: []Edge{{Kind: EdgeDependsOn, Target: "project:other"}},
	}
	got, err := corruptStore(t).Query(Query{Membership: neighbour, Entitlement: PrivacyProject})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for _, r := range got {
		t.Errorf("record %s should have been withheld from a session that does not own it", r.ID)
	}
}

// TestStore_Query_UnresolvableRecordsNarrowRatherThanVanishForTheirOwner
// is the other side of the same guard. The owning session with personal
// entitlement is the one caller entitled to the narrowest classification,
// so it does see those records, and it sees them re-tagged at the
// least-privileged value rather than at whatever the corrupt row claimed.
// The records that cannot resolve a scope or a corpus at all stay withheld
// from everyone.
func TestStore_Query_UnresolvableRecordsNarrowRatherThanVanishForTheirOwner(t *testing.T) {
	got, err := corruptStore(t).Query(Query{Membership: ownMembership(), Entitlement: PrivacyPersonal})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	seen := map[string]Record{}
	for _, r := range got {
		seen[r.ID] = r
	}
	for _, id := range []string{"no-scope", "orphan-corpus", "foreign-scope", "zero-value"} {
		if _, ok := seen[id]; ok {
			t.Errorf("record %s has no resolvable owner and must be withheld from everyone", id)
		}
	}
	for _, id := range []string{"no-visibility", "unknown-visibility", "no-privacy", "unknown-trust"} {
		if _, ok := seen[id]; !ok {
			t.Errorf("record %s should still reach its own owning session", id)
		}
	}
	for _, id := range []string{"no-visibility", "unknown-visibility"} {
		if got := seen[id].Visibility; got != VisibilityPrivate {
			t.Errorf("record %s surfaced as %q, want the narrowed private", id, got.String())
		}
	}
	if got := seen["no-privacy"].Privacy; got != PrivacyPersonal {
		t.Errorf("an unreadable privacy flag surfaced as %q, want personal", got.String())
	}
	if got := seen["unknown-trust"].Trust; got != TrustUntrustedSource {
		t.Errorf("an unreadable trust tag surfaced as %q, want untrusted-source", got.String())
	}
}

// TestAuthorize_ResolvedValuesRideTheReturnedRecord proves the returned
// record carries what was actually decided, so a consumer never has to
// re-derive the classification and cannot disagree with it.
func TestAuthorize_ResolvedValuesRideTheReturnedRecord(t *testing.T) {
	c := validCorpus()
	c.Visibility = VisibilityPrivate
	c.Trust = TrustUntrustedSource
	c.Privacy = PrivacyPersonal
	r := validRecord()
	r.Visibility = VisibilityTeam
	got, ok := authorize(r, c, Query{Membership: ownMembership(), Entitlement: PrivacyPersonal})
	if !ok {
		t.Fatal("the record is in the session's own scope and should be authorized")
	}
	if got.Visibility != VisibilityPrivate {
		t.Errorf("visibility = %q, want the corpus's narrower private", got.Visibility.String())
	}
	if got.Trust != TrustUntrustedSource {
		t.Errorf("trust = %q, want untrusted-source", got.Trust.String())
	}
	if got.Privacy != PrivacyPersonal {
		t.Errorf("privacy = %q, want personal", got.Privacy.String())
	}
}
