package memory

// Purpose: the memory record vocabulary: the ratified MemoryKind taxonomy
//   (R-14.20), the Provenance block that records where a record came from
//   and what its body hashed to when the store last wrote it, and the
//   MemoryEntry record itself with the R-16.7 scope/commit/supersedes/TTL/
//   confidence fields. Validation lives here so every store implementation
//   refuses the same inputs.
// Inputs: caller-supplied record values; the current instant, from the
//   injected Clock (never the wall clock).
// Outputs: validated records, or a typed pkg/cascade error naming exactly
//   which field was refused.
// Constraints: no bare time.Now (the clock gate resolves aliases); no I/O
//   in this file; every value that reaches disk must be deterministic.
// SPORT: G/memory-store (ADD, placeholder per T-1 sport_updates).

import (
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Clock abstracts time.Now so no memory record ever takes its timestamps
// from the wall clock directly. Declared locally (duck-typed) rather than
// imported, matching internal/storage, internal/plugins and
// internal/elevation: it is a structural twin of internal/runtime.Clock
// and internal/testkit.Clock, both of which declare exactly
// Now() time.Time, so their concrete types satisfy this interface with no
// adapter. Declaring it here is also what keeps this package free of any
// dependency on the C config/profile subsystem, which the contract
// requires and which importing internal/runtime for one interface would
// break.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
}

// MemoryKind is the record taxonomy ratified by R-14.20: the four kinds
// the production-proven memory protocol actually uses. It is the first
// path segment of a record's on-disk location, so its values are also
// directory names and are deliberately lowercase and separator-free.
//
// R-14.20 ratifies this exact exported name, and the S-13/S-14 tickets that
// consume it name it verbatim; renaming to Kind to silence revive's
// package-name-stutter hint would break that contract.
//
//nolint:revive // contract-mandated exported name, see the doc comment above
type MemoryKind string

// The four ratified kinds. This set is closed: adding a fifth changes the
// on-disk layout and is a format change, not an additive one.
const (
	// KindUser holds durable facts about the person the system serves.
	KindUser MemoryKind = "user"
	// KindFeedback holds corrections and stated preferences.
	KindFeedback MemoryKind = "feedback"
	// KindProject holds project state, decisions, and lessons.
	KindProject MemoryKind = "project"
	// KindReference holds looked-up material worth keeping.
	KindReference MemoryKind = "reference"
)

// allKinds lists every valid MemoryKind in a fixed order. Iteration order
// of this slice is what any caller listing the whole store walks, so it is
// a slice and not a map: a map would make output order vary per run.
var allKinds = []MemoryKind{KindUser, KindFeedback, KindProject, KindReference}

// AllKinds returns every valid MemoryKind in a stable, documented order.
// The returned slice is a copy, so a caller cannot mutate the taxonomy.
func AllKinds() []MemoryKind {
	out := make([]MemoryKind, len(allKinds))
	copy(out, allKinds)
	return out
}

// Valid reports whether k is one of the four ratified kinds.
func (k MemoryKind) Valid() bool {
	for _, c := range allKinds {
		if k == c {
			return true
		}
	}
	return false
}

// String returns the kind's on-disk spelling.
func (k MemoryKind) String() string { return string(k) }

// ParseKind converts an on-disk or caller-supplied string to a MemoryKind,
// refusing anything outside the ratified set. It fails closed: an unknown
// kind is never mapped to a default, because doing so would file a record
// under a directory its author did not choose.
func ParseKind(s string) (MemoryKind, error) {
	k := MemoryKind(s)
	if !k.Valid() {
		return "", cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidKind,
			"unknown memory kind %q", s)
	}
	return k, nil
}

// Origin records how a record entered the store.
type Origin string

// The three recognized origins.
const (
	// OriginSession means a live session produced the record.
	OriginSession Origin = "session"
	// OriginFile means the record was ingested from a file on disk.
	OriginFile Origin = "file"
	// OriginHarness means the surrounding harness wrote it.
	OriginHarness Origin = "harness"
)

// allOrigins lists every valid Origin in a fixed order, for the same
// determinism reason as allKinds.
var allOrigins = []Origin{OriginSession, OriginFile, OriginHarness}

// Valid reports whether o is one of the three recognized origins.
func (o Origin) Valid() bool {
	for _, c := range allOrigins {
		if o == c {
			return true
		}
	}
	return false
}

// String returns the origin's on-disk spelling.
func (o Origin) String() string { return string(o) }

// ParseOrigin converts a string to an Origin, refusing anything else. Like
// ParseKind it fails closed rather than defaulting.
func ParseOrigin(s string) (Origin, error) {
	o := Origin(s)
	if !o.Valid() {
		return "", cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidOrigin,
			"unknown provenance origin %q", s)
	}
	return o, nil
}

// Provenance records where a record came from and what its body hashed to
// the last time the store wrote it.
//
// ContentHash is a snapshot, not a live checksum: it is the BLAKE3 digest
// of the body AS OF THE LAST STORE WRITE. Because the file is the source
// of truth and a user may edit it with any editor, the body on disk can
// legitimately move on without the store being told. Comparing this field
// against MemoryEntry.BodyHash is therefore the drift signal a downstream
// projection uses to decide what must be re-derived; a mismatch means the
// file was edited outside the store, which is expected, not corruption.
type Provenance struct {
	// Origin is how the record entered the store.
	Origin Origin
	// SessionID identifies the producing session. May be empty when the
	// origin carries no session.
	SessionID string
	// CreatedAt is when the store first wrote this record, from the
	// injected clock. Canonicalized to UTC by the store.
	CreatedAt time.Time
	// UpdatedAt is when the store last rewrote this record's body, from
	// the injected clock. Canonicalized to UTC by the store.
	UpdatedAt time.Time
	// ContentHash is the BLAKE3 hex digest of the body as of that last
	// write. See the type doc for why it may differ from the body on disk.
	ContentHash string
}

// MemoryEntry is one memory record. Name and Kind together are its
// identity and its on-disk location ({base}/{kind}/{name}.md); every other
// field is content or metadata that travels in the file's frontmatter,
// except Body, which is the markdown after the frontmatter fence.
//
// R-16.7 names this exact exported symbol, as do the projection, CLI and
// consolidation tickets built on it; renaming to Entry to silence revive's
// package-name-stutter hint would break that contract.
//
//nolint:revive // contract-mandated exported name, see the doc comment above
type MemoryEntry struct {
	// Name is the record's identifier within its kind. It is also the
	// filename stem, so ValidateName constrains it to a portable charset.
	Name string
	// Kind is the record's taxonomy member and its directory.
	Kind MemoryKind
	// Description is a one-line summary.
	Description string
	// Body is the markdown payload.
	Body string
	// Provenance records origin, session, timestamps, and content hash.
	Provenance Provenance
	// ScopeRef is the scope this record belongs to. Required and
	// syntactic-only in this phase: a non-empty string, with no check
	// against the scope graph, which does not exist yet (R-16.7).
	ScopeRef string
	// CommitSHA optionally pins the record to a commit. Empty means none.
	CommitSHA string
	// Supersedes optionally names the record this one replaces, as a
	// "<kind>/<name>" reference. Empty means none. Syntactic-only: no
	// check that the referenced record exists (R-16.7).
	Supersedes string
	// ExpiresAt optionally gives the record a TTL. Nil means it never
	// expires. Canonicalized to UTC by the store.
	ExpiresAt *time.Time
	// Confidence is how much to trust the record, in [0,1].
	Confidence float64
}

// BodyHash returns the BLAKE3 hex digest of the body currently held in
// this value. Compare it against Provenance.ContentHash to detect a body
// that was edited outside the store: equal means the store's record is
// current, different means the file moved on and anything derived from it
// (an embedding, an index row) must be rebuilt.
func (e MemoryEntry) BodyHash() string { return HashBody(e.Body) }

// Expired reports whether the record's TTL has passed at instant now.
// A record with no TTL never expires.
func (e MemoryEntry) Expired(now time.Time) bool {
	return e.ExpiresAt != nil && !e.ExpiresAt.After(now)
}

// canonical returns a copy of e with every value that reaches disk put in
// its canonical form: timestamps in UTC, so a record written from a
// machine in one time zone and read on another compares equal, and the
// TTL pointer copied rather than shared with the caller.
func (e MemoryEntry) canonical() MemoryEntry {
	out := e
	out.Provenance.CreatedAt = e.Provenance.CreatedAt.UTC()
	out.Provenance.UpdatedAt = e.Provenance.UpdatedAt.UTC()
	if e.ExpiresAt != nil {
		t := e.ExpiresAt.UTC()
		out.ExpiresAt = &t
	}
	return out
}

// Validate refuses any record the store must not write, returning a typed
// pkg/cascade error that names the offending field through a distinct
// sentinel, so a caller can tell the failures apart with errors.Is rather
// than by matching message text. now is the instant the TTL is judged
// against and comes from the caller's injected clock.
func (e MemoryEntry) Validate(now time.Time) error {
	if err := ValidateName(e.Name); err != nil {
		return err
	}
	if !e.Kind.Valid() {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidKind,
			"unknown memory kind %q", string(e.Kind))
	}
	if !e.Provenance.Origin.Valid() {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidOrigin,
			"unknown provenance origin %q", string(e.Provenance.Origin))
	}
	return e.validateFields(now)
}

// validateFields checks the R-16.7 additions. Split from Validate to keep
// both functions inside the 50-line limit.
func (e MemoryEntry) validateFields(now time.Time) error {
	if strings.TrimSpace(e.ScopeRef) == "" {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidScopeRef,
			"scope_ref must be a non-empty reference")
	}
	if e.Supersedes != "" && !validReference(e.Supersedes) {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidSupersedes,
			"supersedes %q is not a <kind>/<name> reference", e.Supersedes)
	}
	if e.Confidence < 0 || e.Confidence > 1 {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidConfidence,
			"confidence %v is outside [0,1]", e.Confidence)
	}
	if e.Expired(now) {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrAlreadyExpired,
			"expires_at %s is not after the current instant %s",
			e.ExpiresAt.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano))
	}
	return nil
}

// validReference reports whether ref has the "<kind>/<name>" shape, with a
// ratified kind and a valid name. This is the syntactic check R-16.7
// permits; it deliberately does NOT ask whether the referenced record
// exists, because the scope graph that would answer that is not built yet
// and a check that silently always passes would be worse than none.
func validReference(ref string) bool {
	kind, name, ok := strings.Cut(ref, "/")
	if !ok {
		return false
	}
	return MemoryKind(kind).Valid() && ValidateName(name) == nil
}
