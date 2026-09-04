// Package fusion runs the query-time retrieval path: it narrows the
// candidate set to what the asking session may see, runs the legs against
// that narrowed set, and hands their ranked output to the ranking core.
//
// Purpose: hold the one place scope is enforced. There is exactly one
// scope-enforcement mechanism in retrieval, ScopeFilter, and it runs
// BEFORE any leg does. Nothing downstream re-checks scope, and nothing
// downstream is allowed to: a filter applied after ranking is a filter
// somebody can forget, and the thing it would be protecting is content
// from another session's scope.
//
// Inputs: the corpus store and the asking session's query (its membership
// and its privacy entitlement).
//
// Outputs: the authorized candidate records, the vector namespaces those
// records live in, and the predicate a full-text leg binds into its own
// query.
//
// Constraints: this package decides nothing about visibility, privacy or
// trust itself. Every authorization decision is the corpus package's,
// taken by calling its scope-filtered query and using the resolved records
// it returns. A second copy of those rules here would be a second place to
// get them wrong, and the copy would be the one that drifted.
//
// SPORT: internal.retrieval.fusion.ScopeFilter/ADDED (P1-E06-W2-S11-T1).
package fusion

import (
	"sort"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
)

// namespacePrefix is the vector-store namespace each corpus's embeddings
// live under. Namespaces are per corpus so a scope decision made over
// corpora translates directly into the set of namespaces a leg is allowed
// to open, rather than into a filter over one shared namespace's results.
const namespacePrefix = "retrieval/corpus/"

// NamespaceFor returns the vector-store namespace holding corpusID's
// embeddings.
func NamespaceFor(corpusID string) string {
	return namespacePrefix + corpusID
}

// ScopePredicate is what a full-text leg binds into its own query so its
// scan is narrowed at the index rather than afterwards. It carries values,
// not SQL: the leg owns its schema and its placeholder syntax, and a
// predicate written here in that leg's dialect would be this package
// guessing at another package's table.
type ScopePredicate struct {
	// CorpusIDs are the corpora the session may read, sorted.
	CorpusIDs []string
	// RecordIDs are the individual records the session may read, sorted.
	// A record can be classified more narrowly than the corpus holding
	// it, so the corpus list alone is not a sufficient narrowing.
	RecordIDs []string
}

// ScopeFilter is the authorized candidate set for one session's query,
// resolved once, before any leg runs.
//
// It is deliberately a snapshot rather than a predicate object the legs
// call back into: the set is computed from the corpus model up front, and
// a leg can only ever be given the narrowed set, never the store.
type ScopeFilter struct {
	records    []corpus.Record
	byID       map[string]corpus.Record
	corpusIDs  []string
	namespaces []string
}

// NewScopeFilter resolves the candidate set for q against store.
//
// The whole authorization decision belongs to store.Query: deny by
// default, per record, before anything else happens to that record. A
// malformed membership is refused there and the refusal is returned here
// rather than being turned into an empty result set, because a caller with
// a broken membership needs to hear about it instead of concluding that
// the index is empty.
func NewScopeFilter(store *corpus.Store, q corpus.Query) (*ScopeFilter, error) {
	if store == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "fusion: nil corpus store")
	}
	records, err := store.Query(q)
	if err != nil {
		return nil, err
	}
	f := &ScopeFilter{byID: make(map[string]corpus.Record, len(records))}
	corpora := make(map[string]bool)
	for _, r := range records {
		f.records = append(f.records, r)
		f.byID[r.ID] = r
		corpora[r.CorpusID] = true
	}
	for id := range corpora {
		f.corpusIDs = append(f.corpusIDs, id)
	}
	sort.Strings(f.corpusIDs)
	for _, id := range f.corpusIDs {
		f.namespaces = append(f.namespaces, NamespaceFor(id))
	}
	return f, nil
}

// Candidates returns the authorized records, each carrying the
// classification the corpus model resolved for it.
func (f *ScopeFilter) Candidates() []corpus.Record {
	return append([]corpus.Record(nil), f.records...)
}

// Namespaces returns the vector-store namespaces the session may open,
// sorted. A leg opens these and only these; it never enumerates the
// store's own namespace list, because that list is every scope's.
func (f *ScopeFilter) Namespaces() []string {
	return append([]string(nil), f.namespaces...)
}

// Predicate returns the narrowing a full-text leg binds into its query.
func (f *ScopeFilter) Predicate() ScopePredicate {
	ids := make([]string, 0, len(f.records))
	for _, r := range f.records {
		ids = append(ids, r.ID)
	}
	sort.Strings(ids)
	return ScopePredicate{
		CorpusIDs: append([]string(nil), f.corpusIDs...),
		RecordIDs: ids,
	}
}

// Empty reports whether the session is authorized to read nothing. A leg
// asks this to skip work, never to decide access.
func (f *ScopeFilter) Empty() bool {
	return len(f.records) == 0
}

// Resolve returns the authorized record for a chunk id a leg's driver
// returned, and reports whether one exists.
//
// This is the binding computed above being enforced on the way back, not a
// second scope decision: a namespace holds a whole corpus, while
// classification is per record, so a driver can hand back an id that the
// corpus model already withheld. An id with no authorized record has no
// resolved classification at all, and unclassified content is withheld,
// exactly as the corpus model withholds it. It runs on the driver's raw
// response, before any ranking or fusion has happened, so no ranked result
// is ever filtered after the fact.
func (f *ScopeFilter) Resolve(chunkID string) (corpus.Record, bool) {
	r, ok := f.byID[chunkID]
	return r, ok
}
