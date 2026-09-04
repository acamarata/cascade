package memory

// Purpose: the projection's schema: the key layout every projected row and
//   posting is written under inside the memory domain, the IndexedRecord
//   that is stored, its codec, the tokenizer the full-text postings are
//   built from.
// Inputs: records handed over by the projection job.
// Outputs: encoded rows and posting keys, or a pkg/cascade taxonomy error.
// Constraints: pure functions plus key-value reads; no clock, no
//   randomness, no direct SQL. Every derived value is deterministic:
//   tokens are lowercased, deduplicated and sorted, and results are
//   ordered by record id, so an identical tree projects to identical bytes
//   and an identical query returns an identical order on any machine.
// SPORT: G/memory-db-projection (ADD, P1-E07-W2-S13-T2).

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// CONTRACT DEVIATION (recorded, not papered over). This ticket's contract
// specifies "schema.go: SQL DDL for the memory domain", a "memories" table
// and a "memories_fts FTS5 virtual table ... content='memories'". The tree
// has moved past that shape. Every cascade.db domain persists through
// pkg/provider.Store, whose SQLite driver keeps ONE physical key-value
// table (providers/sqlite/driver.go), and the production migration set is
// deliberately empty (cmd/cascade/daemon_unix_store.go). internal/audit
// hit the same question one epic earlier and resolved it the same way
// (internal/audit/schema.go header). The contract itself forbids the only
// route to a CREATE VIRTUAL TABLE from here: "storage.Store ... injected,
// no hard import of storage internals" and "no direct sql.DB calls from
// this package". A projection that opened its own sql.DB to build an FTS5
// table would bypass the single write executor that §2 project law
// requires, so the full-text index is built as an inverted token index in
// the same key-value namespace instead: the queryable behaviour the
// contract's acceptance criteria name (a term in a body returns that
// record) is delivered and tested against a real SQLite file, through the
// store abstraction every other domain uses. See the journal for both
// sides quoted.

// ProjectionVersion is the compiled layout version of everything in this
// file: the key layout, the IndexedRecord field set, and the tokenizer.
// Any change to how a record projects must increment it, because a row
// written by an older build cannot be compared against a row this build
// would write, and comparing them anyway is how a projection quietly
// serves a shape nothing understands. A mismatch between this constant
// and the version stamped in the store triggers a full rebuild, which is
// always safe: the files hold everything needed to rebuild.
const ProjectionVersion = 1

// projectionNamespace is the pkg/provider.Store scoping argument every key
// below is written under: the ratified memory domain from R-14.5's closed
// ten, taken from internal/storage rather than re-spelled as a literal.
const projectionNamespace = string(storage.DomainMemory)

// The projection's key layout inside the memory namespace. Every key this
// package writes begins with projectionPrefix, so the projection can be
// dropped whole (Rebuild) without touching anything else the memory domain
// stores, and so it cannot collide with another writer in the namespace.
//
// A row key is "proj:rec:<kind>/<name>": the canonical unit address the
// file store is keyed by, with no separate id space invented for it. A
// posting key is "proj:tok:<token>:<kind>/<name>", scanned by the prefix
// "proj:tok:<token>:" to yield every record carrying that token. Tokens
// are restricted to ASCII letters and digits by the tokenizer, so ":"
// never appears inside one and the split is unambiguous.
const (
	projectionPrefix = "proj:"
	recordPrefix     = projectionPrefix + "rec:"
	tokenPrefix      = projectionPrefix + "tok:"
	metaVersionKey   = projectionPrefix + "meta:version"
)

// postingValue is what a posting key stores. A posting is a membership
// fact and carries no payload: the key is the whole of the information.
// It is an empty non-nil slice rather than nil because the store's value
// column is NOT NULL, and a posting that failed to write would silently
// make its record unfindable by that term.
var postingValue = []byte{}

// ErrProjectionCorrupt is returned when a row read back out of the
// projection cannot be decoded. It is deliberately an integrity refusal
// rather than a silent skip: a query that quietly dropped rows it could
// not read would return a short answer that looks complete. Because the
// files are the source of truth, the response is to rebuild the
// projection, never to repair a row in place.
var ErrProjectionCorrupt = cascade.New(cascade.KindIntegrity, "corrupt memory projection row")

// recordID is a record's canonical address, "<kind>/<name>". It is the row
// key's suffix, the posting key's suffix, and the vector id, so a record
// has exactly one identity across all three.
func recordID(kind MemoryKind, name string) string { return string(kind) + "/" + name }

func recordKey(id string) string        { return recordPrefix + id }
func kindRowPrefix(k MemoryKind) string { return recordPrefix + string(k) + "/" }

func tokenKey(token, id string) string    { return tokenPrefix + token + ":" + id }
func tokenScanPrefix(token string) string { return tokenPrefix + token + ":" }

// IndexedRecord is one record as the projection holds it.
//
// It is DERIVED STATE and nothing else. Every field is a copy of what the
// record's file said when the projection last read it, so a field here can
// legitimately be stale: a user may edit the file with any editor at any
// moment. When the two disagree THE FILE IS AUTHORITATIVE, and the
// response is to run the projection again, never to write the file back
// from a row. Nothing outside the files is needed to reconstruct this.
type IndexedRecord struct {
	// ID is the canonical "<kind>/<name>" address.
	ID string `json:"id"`
	// Name and Kind are the record's identity, split out so a caller
	// need not re-parse ID.
	Name string     `json:"name"`
	Kind MemoryKind `json:"kind"`
	// Description and Body are the indexed text, copied from the file.
	Description string `json:"description"`
	Body        string `json:"body"`
	// Origin and SessionID carry the record's provenance.
	Origin    Origin `json:"origin"`
	SessionID string `json:"session_id,omitempty"`
	// ScopeRef travels with the row so a hit can never be shown to a
	// caller the record's own scope does not admit. An index that dropped
	// it would widen visibility past what the record permits.
	ScopeRef string `json:"scope_ref"`
	// ContentHash is the BLAKE3 digest of the body AS PROJECTED, which is
	// the body that was on disk at that moment. It is what the vector
	// dedupe compares, so an out-of-store edit re-embeds and an unchanged
	// body does not.
	ContentHash string `json:"content_hash"`
	// CreatedAtUnixNano and UpdatedAtUnixNano are the record's own
	// timestamps, as integers so the row's bytes do not depend on a
	// time-zone database.
	CreatedAtUnixNano int64 `json:"created_at_unix_nano"`
	UpdatedAtUnixNano int64 `json:"updated_at_unix_nano"`
	// ExpiresAtUnixNano is the record's TTL, or nil for no TTL.
	ExpiresAtUnixNano *int64 `json:"expires_at_unix_nano,omitempty"`
	// Confidence is the record's stated confidence in [0,1].
	Confidence float64 `json:"confidence"`
	// Deleted marks a row whose record is gone from the files (tombstoned
	// or removed). The row is kept, rather than dropped, so a later run
	// can tell "retired" from "never seen" and so a name that comes back
	// flips the same row live again.
	Deleted bool `json:"deleted"`
	// IndexedAtUnixNano is when the projection last wrote this row, from
	// the injected clock.
	IndexedAtUnixNano int64 `json:"indexed_at_unix_nano"`
	// EmbedModel identifies the embedding space this record's vector was
	// written in, empty when no vector was written. It is stored so two
	// embedding spaces cannot be mixed silently across a model change.
	EmbedModel string `json:"embed_model,omitempty"`
	// Tokens is the exact posting set written for this row, sorted. It is
	// stored so an update can retract precisely the postings it wrote,
	// without scanning the whole token space.
	Tokens []string `json:"tokens,omitempty"`
}

// Visible reports whether a query may return this row at instant at. A
// retired record and an expired one are both invisible. This is the only
// place a query narrows a result set, and it narrows: no field here can
// make a record visible that its own file does not already permit.
func (r IndexedRecord) Visible(at time.Time) bool {
	if r.Deleted {
		return false
	}
	if r.ExpiresAtUnixNano != nil && !time.Unix(0, *r.ExpiresAtUnixNano).UTC().After(at) {
		return false
	}
	return true
}

// encodeRow marshals a row. Field order is the struct's, fixed at compile
// time, so identical rows encode to identical bytes.
func encodeRow(r IndexedRecord) ([]byte, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, ErrProjectionCorrupt,
			"encoding projection row %s: %v", r.ID, err)
	}
	return data, nil
}

// decodeRow unmarshals a row, refusing anything it cannot read whole.
func decodeRow(data []byte) (IndexedRecord, error) {
	var r IndexedRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return IndexedRecord{}, cascade.Wrapf(cascade.KindIntegrity, ErrProjectionCorrupt,
			"decoding projection row: %v", err)
	}
	return r, nil
}

// maxTokenLen bounds one token. A longer run of characters is truncated
// rather than dropped, so a pathological input costs a bounded key size
// instead of making its record unfindable.
const maxTokenLen = 64

// tokenize splits text into the sorted, deduplicated set of lowercase
// alphanumeric tokens the postings are keyed by. It is the whole of the
// full-text analysis: no stemming, no stop words, no language guessing,
// because each of those makes a hit depend on a table that has to match at
// query time and silently changes the result set when it does not.
func tokenize(text string) []string {
	seen := make(map[string]bool)
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			seen[cur.String()] = true
			cur.Reset()
		}
	}
	for _, r := range strings.ToLower(text) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if cur.Len() < maxTokenLen {
				cur.WriteRune(r)
			}
		default:
			flush()
		}
	}
	flush()
	out := make([]string, 0, len(seen))
	for tok := range seen {
		out = append(out, tok)
	}
	sort.Strings(out)
	return out
}

// rowTokens is the posting set for a record: its name, description and
// body together, so a search term matches whichever of the three carries
// it.
func rowTokens(r IndexedRecord) []string {
	return tokenize(r.Name + " " + r.Description + " " + r.Body)
}
