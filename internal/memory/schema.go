package memory

// Purpose: the memory projection's on-record schema. It defines what a
//   projected row is, the key layout the rows and their term postings
//   occupy inside the memory domain namespace, the deterministic
//   tokenizer that turns a record into searchable terms, and the
//   compiled projection version that decides when the whole projection
//   must be thrown away and rebuilt.
// Inputs: validated MemoryEntry values from the file store.
// Outputs: encoded row bytes, namespaced keys, sorted term lists, or a
//   pkg/cascade taxonomy error.
// Constraints: pure functions only, no clock, no I/O, no randomness. Every
//   derived value is order-stable, so projecting the same files twice
//   produces byte-identical rows.
//
//   CONTRACT DEVIATION (recorded, not papered over). This ticket's
//   contract asks for "SQL DDL for the memory domain within cascade.db"
//   with a "memories table", a "memories_fts FTS5 virtual table" and a
//   "projection_meta table", "applied via the B-S03 migration builder".
//   The tree has moved past that. Every cascade.db domain persists
//   through pkg/provider.Store, whose whole surface is Get/Put/Delete/
//   Scan/Tx over one physical kv table (providers/sqlite/driver.go
//   schemaDDL); it has no seam through which this package could issue a
//   CREATE VIRTUAL TABLE or an FTS5 MATCH, and the production migration
//   set is deliberately empty (cmd/cascade/daemon_unix_store.go: "adding
//   speculative steps with nothing to migrate would be its own Article-1
//   violation"). A CREATE TABLE step registered here would emit tables no
//   code in this package can read. This file therefore defines the row
//   schema and the inverted term index that are really enforced, both
//   built on the Store surface the tree actually has, and the domain's
//   anchor table stays storage.Bootstrap's, as it is for every other
//   domain. When a SQL seam exists, the FTS5 leg named by
//   internal/retrieval/rrf.StrategyFTS replaces this index and the row
//   schema below is what it is built from. See the journal for both
//   sides quoted.
// SPORT: G/memory-db-projection (ADD, placeholder per T-2 sport_updates).

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ProjectionVersion is the compiled schema version of the rows this build
// writes. Bump it whenever the row shape, the key layout or the tokenizer
// changes: a stored projection stamped with a different version is not
// read and not migrated, it is dropped and rebuilt from the files, which
// is always available because the files are the source of truth.
const ProjectionVersion = 1

// projNamespace is the pkg/provider.Store scoping argument every key this
// package writes lives under: the ratified memory domain from R-14.5's
// closed ten, taken from internal/storage rather than re-spelled as a
// literal.
const projNamespace = string(storage.DomainMemory)

// Key prefixes inside the memory namespace. They are all under one "proj:"
// prefix so the whole projection can be dropped by a single prefix scan,
// and so a future non-derived memory key in this domain cannot collide
// with a projection key.
const (
	rowPrefix  = "proj:row:"
	termPrefix = "proj:term:"
	metaKey    = "proj:meta"
)

// termSep separates a term from the address it points at inside a posting
// key. The tokenizer emits lowercase ASCII alphanumerics only and an
// address is "<kind>/<name>" over the ValidateName charset, so this byte
// appears in neither half and the split is unambiguous.
const termSep = "|"

// maxTermLen caps one indexed term. A pathological run of characters with
// no separator is one term, not a reason for a key the store must carry
// forever; the prefix that survives still matches the same record.
const maxTermLen = 64

// Address returns a record's canonical unit address, "<kind>/<name>". It
// is the projection's row identity: the file store is keyed by kind and
// name, and no separate identifier space exists for a memory record.
func Address(kind MemoryKind, name string) string {
	return string(kind) + "/" + name
}

// rowKey returns the store key holding one record's projected row.
func rowKey(addr string) string { return rowPrefix + addr }

// termKey returns the posting key that records "this term occurs in this
// record". The value is empty: the key IS the fact, so a posting costs one
// key and a term's postings are one prefix scan.
func termKey(term, addr string) string { return termPrefix + term + termSep + addr }

// termScanPrefix returns the prefix that enumerates every posting for one
// term. The separator is part of the prefix so that scanning "cat" cannot
// also return the postings of "catalogue".
func termScanPrefix(term string) string { return termPrefix + term + termSep }

// addressFromPosting recovers the address half of a posting key.
func addressFromPosting(key string) (string, bool) {
	_, addr, ok := strings.Cut(strings.TrimPrefix(key, termPrefix), termSep)
	return addr, ok
}

// ProjectionRow is one record as the projection holds it: enough to find
// and rank the record, and deliberately not enough to reconstruct it.
//
// The body is NOT stored here. The file is the record, and a second copy
// of the body in a derived store is a copy that can silently disagree with
// the file it was derived from. A search returns addresses and metadata; a
// caller that wants the record reads it back through the store, where the
// file wins and a damaged or unsupported record is refused as it would be
// on any other read. ContentHash is what lets a later run tell that the
// file moved on without re-reading the body.
type ProjectionRow struct {
	// Address is "<kind>/<name>", the row's identity.
	Address string `json:"address"`
	// Name and Kind are the file store's key for this record.
	Name string     `json:"name"`
	Kind MemoryKind `json:"kind"`
	// Description is the record's one-line summary, indexed and returned.
	Description string `json:"description"`
	// ContentHash is the BLAKE3 digest of the body this row was derived
	// from. A file whose body now hashes differently is re-projected.
	ContentHash string `json:"content_hash"`
	// Origin and SessionID carry the record's provenance.
	Origin    Origin `json:"origin"`
	SessionID string `json:"session_id,omitempty"`
	// ScopeRef, Supersedes, CommitSHA and Confidence are the R-16.7
	// fields a recall caller filters and ranks on.
	ScopeRef   string  `json:"scope_ref"`
	Supersedes string  `json:"supersedes,omitempty"`
	CommitSHA  string  `json:"commit_sha,omitempty"`
	Confidence float64 `json:"confidence"`
	// CreatedAtUnixNano and UpdatedAtUnixNano are the record's own
	// timestamps, copied from its provenance. They are NOT the time this
	// row was written: no row carries a reading of the projection's clock,
	// which is what makes two rebuilds of the same files byte-identical.
	CreatedAtUnixNano int64 `json:"created_at_unix_nano"`
	UpdatedAtUnixNano int64 `json:"updated_at_unix_nano"`
	// ExpiresAtUnixNano is the record's TTL, or zero for none.
	ExpiresAtUnixNano int64 `json:"expires_at_unix_nano,omitempty"`
	// Deleted marks a record the files no longer have. The row is kept so
	// a reader can tell "retired" from "never seen", but every posting is
	// removed, so a deleted record can never be returned by a search.
	Deleted bool `json:"deleted,omitempty"`
	// Terms is the sorted, de-duplicated term list this row's postings
	// were written from. It is stored so an update can delete exactly the
	// postings it replaces without scanning the whole index.
	Terms []string `json:"terms,omitempty"`
}

// projectionMeta is the projection's own header row.
type projectionMeta struct {
	// Version is the ProjectionVersion the stored rows were written by. A
	// mismatch means the rows are unreadable by this build and the
	// projection is rebuilt rather than migrated.
	Version int `json:"version"`
	// ProjectedAtUnixNano is the injected clock's instant at the end of
	// the last run. It is the only clock reading the projection stores,
	// and it is deliberately outside the rows.
	ProjectedAtUnixNano int64 `json:"projected_at_unix_nano"`
}

// rowFor derives a live row from a record. It is a pure function of the
// record, so the same file always yields the same bytes.
func rowFor(e MemoryEntry) ProjectionRow {
	row := ProjectionRow{
		Address:           Address(e.Kind, e.Name),
		Name:              e.Name,
		Kind:              e.Kind,
		Description:       e.Description,
		ContentHash:       HashBody(e.Body),
		Origin:            e.Provenance.Origin,
		SessionID:         e.Provenance.SessionID,
		ScopeRef:          e.ScopeRef,
		Supersedes:        e.Supersedes,
		CommitSHA:         e.CommitSHA,
		Confidence:        e.Confidence,
		CreatedAtUnixNano: e.Provenance.CreatedAt.UTC().UnixNano(),
		UpdatedAtUnixNano: e.Provenance.UpdatedAt.UTC().UnixNano(),
		Terms:             Tokenize(e.Name + " " + e.Description + " " + e.Body),
	}
	if e.ExpiresAt != nil {
		row.ExpiresAtUnixNano = e.ExpiresAt.UTC().UnixNano()
	}
	return row
}

// encodeRow renders a row for storage.
func encodeRow(row ProjectionRow) ([]byte, error) {
	data, err := json.Marshal(row)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindIntegrity, ErrMalformedEntry,
			"encoding projected row %s: %v", row.Address, err)
	}
	return data, nil
}

// decodeRow parses a stored row. It fails closed: a row this build cannot
// parse whole is refused, never returned half-populated, on the same terms
// as a record read from a file.
func decodeRow(data []byte) (ProjectionRow, error) {
	var row ProjectionRow
	if err := json.Unmarshal(data, &row); err != nil {
		return ProjectionRow{}, cascade.Wrapf(cascade.KindIntegrity, ErrMalformedEntry,
			"decoding projected row: %v", err)
	}
	return row, nil
}

// Tokenize returns the sorted, de-duplicated set of searchable terms in
// text: maximal runs of ASCII letters and digits, lower-cased, each capped
// at maxTermLen bytes.
//
// It is deliberately small and deliberately deterministic. No stemming, no
// stop-word list and no locale: every one of those makes the index depend
// on a table that can change between builds, and a term index that a
// rebuild does not reproduce exactly is a projection that cannot be
// verified against the files it came from.
func Tokenize(text string) []string {
	seen := make(map[string]bool)
	var cur strings.Builder
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		seen[cur.String()] = true
		cur.Reset()
	}
	for _, r := range text {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			appendTermRune(&cur, r, flush)
		case r >= 'A' && r <= 'Z':
			appendTermRune(&cur, r+('a'-'A'), flush)
		default:
			flush()
		}
	}
	flush()
	out := make([]string, 0, len(seen))
	for term := range seen {
		out = append(out, term)
	}
	sort.Strings(out)
	return out
}

// appendTermRune adds one rune to the term under construction, flushing
// first when the term has reached maxTermLen so a pathological run splits
// into whole terms rather than growing without bound.
func appendTermRune(cur *strings.Builder, r rune, flush func()) {
	if cur.Len() >= maxTermLen {
		flush()
	}
	cur.WriteRune(r)
}
