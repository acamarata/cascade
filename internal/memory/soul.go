package memory

// Purpose: the SOUL vocabulary — the persistent identity document, the
//   three sanctioned edit routes, the audit entry every write appends, the
//   divergence report route (b) produces, and the export envelope. The
//   SOUL is the system's own model of the person it serves, so this file
//   is written to one rule above all others: nothing that describes a
//   change to the SOUL may carry the SOUL's text.
// Inputs: caller-supplied documents; instants from the injected Clock.
// Outputs: validated values and typed pkg/cascade errors; no I/O here.
// Constraints: no clock read, no file system, no map reaching any encoded
//   output; every JSON tag is fixed at compile time so identical input
//   exports to identical bytes.
// SPORT: G/memory-soul-store (ADD, placeholder per T-2 sport_updates).

import (
	"context"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// SoulSchemaVersion is the version of the EXPORT envelope this build
// writes, and DefaultSoulSchema is the identifier stamped on a document
// that names no schema of its own.
//
// They are two different versions on purpose. The envelope version says
// how the exported JSON is laid out; the document schema says what the
// body is expected to contain. Collapsing them would make a change to the
// export format look like a change to the user's own document.
const (
	SoulSchemaVersion = 1
	DefaultSoulSchema = "cascade.soul/v1"
)

// maxSoulBodyBytes bounds the document. A SOUL is a page a person writes
// about themselves, not a corpus; the bound exists so a runaway writer
// cannot turn the identity document into an unreadable, unexportable file.
const maxSoulBodyBytes = 1 << 20

// SoulNoInputMessage is the wording of the CASCADE_NO_INPUT refusal.
//
// It says "hard error" deliberately and that wording is load-bearing: the
// automation-parity rule (§5 rule 8) requires the editor path to FAIL,
// visibly and immediately, rather than open a subprocess that waits on a
// terminal no automation has. A message that read like a warning would
// leave an operator looking for the hang instead of for the flag.
const SoulNoInputMessage = "CASCADE_NO_INPUT=1 makes opening $EDITOR a hard error; " +
	"pass --content <file> to apply a document without an editor"

// Domain sentinels. Each names one refusal precisely so a caller matches
// with errors.Is rather than on message text, and each wraps exactly one
// frozen pkg/cascade Kind; no Kind is invented here.
var (
	// ErrNoSoulDocument is returned when no SOUL has ever been written.
	ErrNoSoulDocument = cascade.New(cascade.KindNotFound, "no soul document")
	// ErrInvalidSoulDocument is returned for a document the store must
	// not write: an empty body, or one past the size bound.
	ErrInvalidSoulDocument = cascade.New(cascade.KindInvalidInput, "invalid soul document")
	// ErrInvalidSoulRoute is returned for an edit route outside the three
	// sanctioned ones. The set is closed: a fourth route would be a fourth
	// way for the system's model of the user to change unaudited.
	ErrInvalidSoulRoute = cascade.New(cascade.KindInvalidInput, "invalid soul edit route")
	// ErrSoulDiverged is returned when the file and the store have BOTH
	// moved since the last reconcile. It is a refusal, not a merge: the
	// store cannot know which side the user meant to keep, and guessing
	// would silently discard one of them.
	ErrSoulDiverged = cascade.New(cascade.KindConflict, "soul document diverged")
	// ErrSoulEditNeedsInput is the refusal `cascade memory soul edit`
	// raises when CASCADE_NO_INPUT=1 forbids opening an editor. It lives
	// here, with the rest of the SOUL vocabulary, so the CLI and this
	// package cannot disagree about what that refusal is.
	ErrSoulEditNeedsInput = cascade.New(cascade.KindInvalidInput, SoulNoInputMessage)
	// ErrMalformedSoulLedger is returned when the ledger file exists but
	// cannot be parsed whole — a truncated or damaged write.
	ErrMalformedSoulLedger = cascade.New(cascade.KindIntegrity, "malformed soul ledger")
	// ErrUnsupportedSoulFormat is returned for a ledger declaring a format
	// version this build does not know. It is deliberately separate from
	// ErrMalformedSoulLedger: this one is the forward-compatibility
	// refusal, a file written by a NEWER build, and reading it loosely
	// would silently drop whatever that build recorded.
	ErrUnsupportedSoulFormat = cascade.New(cascade.KindUnsupported, "unsupported soul ledger format")
)

// SoulDocument is the persistent identity document: what the system holds
// to be true about the person it serves.
//
// Body is prose the user owns and may edit with any editor. Schema names
// the shape that body is expected to follow, so a later build can tell a
// v1 document from a v2 one without guessing from its text.
type SoulDocument struct {
	// Body is the document text.
	Body string `json:"body"`
	// Schema identifies the document's expected shape.
	Schema string `json:"schema"`
}

// canonical returns d with its schema defaulted. It does not touch Body:
// the body is the user's own words and is stored exactly as given, so a
// round trip through the store returns what was put in.
func (d SoulDocument) canonical() SoulDocument {
	if strings.TrimSpace(d.Schema) == "" {
		d.Schema = DefaultSoulSchema
	}
	return d
}

// Validate refuses a document the store must not write.
//
// An empty body is refused rather than stored. A SOUL that reads as empty
// is not a neutral state: every consumer of it would take "the system
// knows nothing about this person" as fact, which is a wrong model of the
// user rather than an absent one.
func (d SoulDocument) Validate() error {
	if strings.TrimSpace(d.Body) == "" {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidSoulDocument,
			"soul body is empty")
	}
	if len(d.Body) > maxSoulBodyBytes {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidSoulDocument,
			"soul body is %d bytes, past the %d-byte bound", len(d.Body), maxSoulBodyBytes)
	}
	return nil
}

// SoulEditRoute names how an edit reached the store. The set is closed at
// three: the CLI verb, the reconcile of an out-of-store file edit, and the
// chat-mediated API. Every one of them calls the same audited internal
// path, so no route exists through which the SOUL changes unrecorded.
type SoulEditRoute string

// The three sanctioned routes.
const (
	// SoulRouteCLI is `cascade memory soul edit`.
	SoulRouteCLI SoulEditRoute = "cli"
	// SoulRouteFileReconcile is an edit made directly to the file on disk
	// and adopted by the store on load.
	SoulRouteFileReconcile SoulEditRoute = "file-reconcile"
	// SoulRouteChat is the chat-mediated API, SoulStore.EditViaChat.
	SoulRouteChat SoulEditRoute = "chat"
)

// allSoulRoutes lists every route in a fixed order, so anything derived
// from the set is ordered by a slice rather than by map iteration.
var allSoulRoutes = []SoulEditRoute{SoulRouteCLI, SoulRouteFileReconcile, SoulRouteChat}

// Valid reports whether r is one of the three sanctioned routes.
func (r SoulEditRoute) Valid() bool {
	for _, c := range allSoulRoutes {
		if r == c {
			return true
		}
	}
	return false
}

// String returns the route's stored spelling.
func (r SoulEditRoute) String() string { return string(r) }

// ParseSoulEditRoute converts a stored or caller-supplied string to a
// route, refusing anything else. It fails closed: an unknown route is
// never mapped to a default, because attributing a change to the wrong
// route is worse than refusing to attribute it at all.
func ParseSoulEditRoute(s string) (SoulEditRoute, error) {
	r := SoulEditRoute(s)
	if !r.Valid() {
		return "", cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidSoulRoute,
			"unknown soul edit route %q", s)
	}
	return r, nil
}

// AuditEntry is one recorded change to the SOUL.
//
// # It carries no soul content, by construction
//
// An audit trail of the system's model of a user is itself sensitive: it
// is a history of what the system believed about someone and when. This
// record therefore holds a version, a route, an instant, and a digest —
// and no body, no diff, no excerpt. A reader can prove WHICH change
// happened and in what order; they cannot read the change out of the log.
// The export ships this whole slice, so anything added here leaves the
// machine with it.
type AuditEntry struct {
	// Version is the SOUL version this entry created. Versions increase
	// by exactly one per write across all three routes.
	Version int `json:"version"`
	// Route is how the change reached the store.
	Route SoulEditRoute `json:"route"`
	// EditedAt is the instant of the change, from the injected clock,
	// canonicalized to UTC.
	EditedAt time.Time `json:"edited_at"`
	// DeltaHash identifies the TRANSITION, not the content: it is the
	// BLAKE3 digest of "<previous content hash>:<new content hash>". Two
	// different edits produce different values and the same edit always
	// produces the same one, so the log is verifiable, while the body
	// itself is not recoverable from it.
	DeltaHash string `json:"delta_hash"`
}

// SoulView is the SOUL as a reader sees it: the document, the version it
// is at, and whether the store currently believes the file and the store
// have diverged.
type SoulView struct {
	// Document is the stored document.
	Document SoulDocument `json:"document"`
	// Version is the current version, 0 before the first write.
	Version int `json:"version"`
	// Diverged reports an unresolved conflict between the file and the
	// store. It is surfaced rather than resolved: the reader is told the
	// document in hand may not be the one on disk.
	Diverged bool `json:"diverged"`
}

// SoulExport is the whole of what `cascade memory soul export` writes.
//
// # This type IS the blast radius of an export
//
// Export is the single most dangerous operation in this package: it takes
// the system's model of a person and puts it in a file that leaves the
// machine. What that file contains is exactly this struct and nothing
// else — no other record, no neighbouring memory, no file path, no
// environment, no machine identity. A field added here is a field that
// leaves with every future export, so the bar for adding one is that the
// user would want it in a copy of their own identity document.
type SoulExport struct {
	// SchemaVersion is the envelope version, SoulSchemaVersion.
	SchemaVersion int `json:"schema_version"`
	// ExportedAt is when the export was produced, from the injected
	// clock, in UTC.
	ExportedAt time.Time `json:"exported_at"`
	// Soul is the current document.
	Soul SoulDocument `json:"soul"`
	// AuditEntries is the full change log in version order.
	AuditEntries []AuditEntry `json:"audit_entries"`
}

// SoulStore is the audited SOUL contract. Every write goes through it, and
// every write it accepts increments the version by exactly one and appends
// exactly one AuditEntry.
type SoulStore interface {
	// Get returns the current SOUL, reconciling an out-of-store file edit
	// on the way (route b) so a reader is never handed a document the
	// file has silently moved past.
	Get(ctx context.Context) (SoulView, error)
	// Edit applies doc through route (a), the CLI verb.
	Edit(ctx context.Context, doc SoulDocument) (SoulView, error)
	// EditViaChat applies doc through route (c), the chat-mediated API.
	EditViaChat(ctx context.Context, doc SoulDocument) error
	// DetectDivergence performs the reconcile-on-load of route (b) and
	// reports what it found.
	DetectDivergence(ctx context.Context) (DivergenceResult, error)
	// Export serialises the current document and the whole audit log.
	Export(ctx context.Context) (SoulExport, error)
}
