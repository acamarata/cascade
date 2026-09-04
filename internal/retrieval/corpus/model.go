// Package corpus defines the corpus and scope model retrieval is built
// on: corpora as first-class records with scope membership, the privacy
// flag and shared-visibility class each one carries, and the
// trusted | untrusted-source TRUST dimension that must survive every hop
// to the consumer that decides whether to obey the content.
//
// Purpose: one typed place where "which content may this session see, and
// how far may I trust it" is expressed as data. Query-time fusion, the
// recall command surface, the retrieval config surface, the team
// capability and grant wiring, context assembly and the auto-advance
// ceiling all consume this model; none of them are implemented here, and
// this package deliberately reaches none of them.
//
// Inputs: corpus and record definitions, and a Query carrying the asking
// session's Membership and its privacy entitlement.
//
// Outputs: the subset of records the session is authorized to see, each
// carrying its resolved trust tag, privacy flag and visibility class.
//
// Constraints: validation is strict on the write path (an invalid enum or
// scope value is refused, never coerced) and fail-closed on the read path
// (a record whose classification cannot be resolved is withheld, never
// surfaced). The two directions are deliberate: a caller writing a bad
// value is a bug that must be reported, while a bad value already in the
// store must not become a leak.
//
// SPORT: internal.retrieval.corpus.Corpus/ADDED.
package corpus

import "github.com/acamarata/cascade/pkg/cascade"

// Corpus is one indexed body of content, owned by exactly one scope.
//
// The corpus is the outer bound on everything in it: a record can be
// narrower than its corpus on any of the three classification axes, and
// never wider.
type Corpus struct {
	// ID identifies the corpus. Non-empty and unique within a Store.
	ID string `json:"id"`
	// ScopeRef is the session scope that owns this corpus.
	ScopeRef ScopeRef `json:"scope_ref"`
	// Privacy is the corpus-level privacy flag.
	Privacy PrivacyClass `json:"privacy"`
	// Visibility is the corpus-level shared-visibility class, the outer
	// bound on how far any record in it may travel.
	Visibility VisibilityClass `json:"visibility"`
	// Trust is the corpus source's TRUST classification, the ceiling on
	// how trusted any record carved from it can be.
	Trust TrustLevel `json:"trust"`
}

// Validate refuses a corpus that is not fully classified. There is no
// default for any of the three axes: a corpus definition that omits one is
// rejected rather than being assigned a value the author did not write.
func (c Corpus) Validate() error {
	if c.ID == "" {
		return cascade.New(cascade.KindInvalidInput, "corpus: corpus has no id")
	}
	if !c.ScopeRef.Valid() {
		return cascade.Newf(cascade.KindInvalidInput,
			"corpus %s: invalid scope reference %s", c.ID, describeScope(c.ScopeRef))
	}
	if !c.Privacy.Valid() {
		return cascade.Newf(cascade.KindInvalidInput,
			"corpus %s: %q is not a privacy class", c.ID, string(c.Privacy))
	}
	if !c.Visibility.Valid() {
		return cascade.Newf(cascade.KindInvalidInput,
			"corpus %s: %q is not a visibility class", c.ID, string(c.Visibility))
	}
	if !c.Trust.Valid() {
		return cascade.Newf(cascade.KindInvalidInput,
			"corpus %s: %q is not a trust level", c.ID, string(c.Trust))
	}
	return nil
}

// Record is one indexed unit of content inside a corpus, carrying its own
// scope reference and its own classification on all three axes.
//
// Content itself is not held here. This model is the authorization and
// provenance surface; the chunk text, its index rows and its embeddings
// live in the ingest, index and embed paths.
type Record struct {
	// ID identifies the record, typically the content-addressed chunk id.
	ID string `json:"id"`
	// CorpusID names the corpus this record belongs to.
	CorpusID string `json:"corpus_id"`
	// ScopeRef is the session scope that owns this record. It may be
	// narrower than the corpus's scope; it is never a scope the corpus
	// does not reach, which is why the query decision consults both.
	ScopeRef ScopeRef `json:"scope_ref"`
	// Privacy is the record-level privacy flag.
	Privacy PrivacyClass `json:"privacy"`
	// Visibility is the record-level shared-visibility class.
	Visibility VisibilityClass `json:"visibility"`
	// Trust is the record-level TRUST tag. Every record surfaced through
	// Store.Query carries this tag, resolved against its corpus, so the
	// consumer that decides whether to act on the content can see where
	// the content came from.
	Trust TrustLevel `json:"trust"`
}

// Validate refuses a record that is not fully classified, on the same
// no-defaults rule as Corpus.Validate.
func (r Record) Validate() error {
	if r.ID == "" {
		return cascade.New(cascade.KindInvalidInput, "corpus: record has no id")
	}
	if r.CorpusID == "" {
		return cascade.Newf(cascade.KindInvalidInput, "record %s: no corpus id", r.ID)
	}
	if !r.ScopeRef.Valid() {
		return cascade.Newf(cascade.KindInvalidInput,
			"record %s: invalid scope reference %s", r.ID, describeScope(r.ScopeRef))
	}
	if !r.Privacy.Valid() {
		return cascade.Newf(cascade.KindInvalidInput,
			"record %s: %q is not a privacy class", r.ID, string(r.Privacy))
	}
	if !r.Visibility.Valid() {
		return cascade.Newf(cascade.KindInvalidInput,
			"record %s: %q is not a visibility class", r.ID, string(r.Visibility))
	}
	if !r.Trust.Valid() {
		return cascade.Newf(cascade.KindInvalidInput,
			"record %s: %q is not a trust level", r.ID, string(r.Trust))
	}
	return nil
}

// Query is one session's request against the model. It carries the whole
// authorization input as one value so a caller cannot omit half of it and
// get a wider answer than it is entitled to.
type Query struct {
	// Membership is the asking session's scope, chain and declared edges.
	Membership Membership `json:"membership"`
	// Entitlement is the highest privacy tier this query may see.
	// A query that has not established personal entitlement never
	// receives personal-tier content, whatever its membership.
	Entitlement PrivacyClass `json:"entitlement"`
	// CorpusIDs optionally narrows the query to named corpora. Empty
	// means every corpus the membership already authorizes; naming a
	// corpus never widens the membership decision, it only narrows.
	CorpusIDs []string `json:"corpus_ids,omitempty"`
}

// Store holds corpora and their records and answers scope-filtered
// queries over them. It is the in-memory model surface; the persistent
// index and its query legs are separate tickets, and they narrow their own
// candidate sets with the same Membership rules this Store applies.
type Store struct {
	corpora map[string]Corpus
	records []Record
}

// NewStore returns an empty Store. An empty Store answers every query with
// no records, which is the correct empty-corpus behavior: nothing indexed
// means nothing authorized, not everything authorized.
func NewStore() *Store {
	return &Store{corpora: map[string]Corpus{}}
}

// AddCorpus validates and stores a corpus. A second corpus with the same
// id is a conflict, not an overwrite: silently replacing a corpus would
// silently replace its classification.
func (s *Store) AddCorpus(c Corpus) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if _, exists := s.corpora[c.ID]; exists {
		return cascade.Newf(cascade.KindConflict, "corpus %s: already present", c.ID)
	}
	s.corpora[c.ID] = c
	return nil
}

// AddRecord validates a record and stores it against its corpus. A record
// naming a corpus the Store does not hold is refused, because a record
// with no corpus has no classification ceiling to be bounded by.
func (s *Store) AddRecord(r Record) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if _, ok := s.corpora[r.CorpusID]; !ok {
		return cascade.Newf(cascade.KindNotFound,
			"record %s: corpus %s is not in this store", r.ID, r.CorpusID)
	}
	s.records = append(s.records, r)
	return nil
}

// Query returns the records this session is authorized to see, in
// insertion order, each carrying its resolved classification.
//
// The decision is deny-by-default and is made per record before anything
// else happens to it: a record is included only when its corpus is
// present, its effective visibility class reaches the session's membership
// from the record's own scope, and the query's entitlement permits the
// record's effective privacy tier. A record whose stored classification
// does not resolve is withheld. A malformed Membership is refused outright
// rather than silently matching nothing, so a caller with a broken
// membership hears about it instead of concluding the index is empty.
func (s *Store) Query(q Query) ([]Record, error) {
	if err := q.Membership.Validate(); err != nil {
		return nil, err
	}
	wanted := corpusFilter(q.CorpusIDs)
	var out []Record
	for _, r := range s.records {
		if wanted != nil && !wanted[r.CorpusID] {
			continue
		}
		c, ok := s.corpora[r.CorpusID]
		if !ok {
			continue
		}
		resolved, allowed := authorize(r, c, q)
		if allowed {
			out = append(out, resolved)
		}
	}
	return out, nil
}

// corpusFilter turns the requested corpus ids into a set, or nil when the
// query named none. Ids that name no corpus in the Store simply match
// nothing; naming a corpus is never a grant.
func corpusFilter(ids []string) map[string]bool {
	if len(ids) == 0 {
		return nil
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// authorize resolves a record against its corpus and decides whether the
// query may see it. The returned Record carries the RESOLVED trust,
// privacy and visibility values, so a consumer reads what was actually
// decided rather than re-deriving it and possibly disagreeing.
//
// Every unresolvable input lands on the deny side: an invalid scope
// reference reaches nothing, an unresolvable visibility class collapses to
// private, an unresolvable privacy flag collapses to personal, and an
// unresolvable trust tag collapses to untrusted-source. Nothing here can
// turn an unreadable value into a permission.
func authorize(r Record, c Corpus, q Query) (Record, bool) {
	owner := r.ScopeRef
	if !owner.Valid() {
		return Record{}, false
	}
	// The corpus's own scope bounds the record's: a record cannot be
	// reached through a scope its corpus does not itself sit in or reach.
	if owner != c.ScopeRef && !q.Membership.inChain(c.ScopeRef) && !q.Membership.acrossEdge(c.ScopeRef) {
		return Record{}, false
	}
	visibility := resolveVisibility(r.Visibility, c.Visibility)
	if !visibility.reaches(q.Membership, owner) {
		return Record{}, false
	}
	privacy := resolvePrivacy(r.Privacy, c.Privacy)
	if !q.Entitlement.permits(privacy) {
		return Record{}, false
	}
	r.Privacy = privacy
	r.Visibility = visibility
	r.Trust = resolveTrust(r.Trust, c.Trust)
	return r, true
}
