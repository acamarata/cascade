package conversation

// Purpose: this package's sentinel errors. Every message below is a
//   static string literal -- none is built with fmt.Sprintf/Newf over a
//   Turn.Role, Segment.Kind, or (especially) Segment.Content value. That is
//   deliberate, not an oversight: Content carries conversation user data,
//   and R-14.3's taxonomy errors are exactly what reaches CLI stderr,
//   RPC/JSON-RPC responses, and (via cascade.Error's own logging
//   integration elsewhere in the tree) structured log fields. An error
//   that echoed Content back would leak conversation content through every
//   one of those surfaces. Errors that DO carry caller-supplied
//   identifiers (store.go's out-of-order/not-found paths) use only IDs
//   (content-addressed hashes, never free text) -- see
//   conversation_test.go's TestErrorsNeverEchoContent for the enforced
//   proof.
// SPORT: internal.conversation.errors/ADDED (P1-E20-W5-S43-T1).

import "github.com/acamarata/cascade/pkg/cascade"

// ErrImmutable reports that Append was called with a turn or segment id
// that already exists. The append-only invariant is structural (each
// table's PRIMARY KEY refuses the duplicate row before this package's own
// pre-check even matters -- see domain.go's turnTableStep/segmentTableStep
// doc comments): this error is the typed translation of that structural
// refusal, never an advisory-only check a caller could bypass by calling
// a different method.
var ErrImmutable = cascade.New(cascade.KindConflict, "conversation: append-only: id already exists")

// ErrOutOfOrder reports that Append was called with a Seq that does not
// extend the existing sequence by exactly one -- a gap, a repeat, or a
// value below the next expected position. Refused rather than accepted:
// a corrupt or out-of-order append would otherwise leave ListTurns/
// ListSegments unable to promise insertion order.
var ErrOutOfOrder = cascade.New(cascade.KindInvalidInput, "conversation: append out of order: seq does not extend the sequence")

// ErrTurnNotFound reports that AppendSegment referenced a turn id with no
// corresponding row.
var ErrTurnNotFound = cascade.New(cascade.KindNotFound, "conversation: turn not found")

// ErrInvalidRecord reports that a Turn or Segment failed structural
// validation before any storage operation was attempted (empty id,
// unknown Role/SegmentKind, empty thread/turn reference, non-positive
// CreatedAt, or a negative Seq). Fail-closed: unparseable or incomplete
// input is refused, never defaulted.
var ErrInvalidRecord = cascade.New(cascade.KindInvalidInput, "conversation: invalid record")

// ErrMalformedTurnPayload reports that a chat.append_turn JSON-RPC
// request could not be decoded into appendTurnParams (unknown field,
// wrong type, missing thread_id/role). Static message only -- see this
// file's PRIVACY doc comment; the raw params bytes are never echoed.
var ErrMalformedTurnPayload = cascade.New(cascade.KindInvalidInput, "conversation: malformed chat.append_turn payload")

// ErrEgressSubstitutionFailed reports that the H/S-16.T1 substitution
// pass over a conversation.turn_appended SSE payload failed. Fails
// closed: AppendTurn's storage write has already committed by the time
// this can fire (T1's append-only guarantee is independent of the SSE
// mirror), but no event is ever emitted unsubstituted.
var ErrEgressSubstitutionFailed = cascade.New(cascade.KindUnavailable, "conversation: egress substitution failed for turn_appended event")

// ErrSSEWriteFailed reports that publishing conversation.turn_appended
// to the SSE bridge failed after substitution succeeded.
var ErrSSEWriteFailed = cascade.New(cascade.KindUnavailable, "conversation: sse write failed for turn_appended event")

// ErrSSEUnavailableOnEmbedded reports that the SSE mirror was requested
// while running in Windows tier-2 daemonless/embedded one-shot mode,
// where no SSE bridge exists at all (06-FORGE-SPEC §2). chat.append_turn
// itself still succeeds and the turn is persisted; only the mirror is
// refused. See sse.go's RefuseSSEOnEmbedded.
var ErrSSEUnavailableOnEmbedded = cascade.New(cascade.KindUnsupported, "conversation: SSE mirror unavailable in embedded one-shot mode (Windows tier-2)")

// ErrBadCursor reports that a PaginationFilter.Cursor could not be
// decoded: wrong opaque-token prefix, invalid base64url, unparseable
// JSON, or a turn cursor whose encoded thread id does not match the
// threadID a ListTurnsPage call was made with. Fail-closed, never a
// panic -- see pagination.go's decodeCursor and cursor_fuzz_test.go's
// FuzzCursorDecode for the enforced proof over adversarial bytes.
var ErrBadCursor = cascade.New(cascade.KindInvalidInput, "conversation: malformed pagination cursor")

// ErrSearchUnavailable reports that SearchTurns was called against a
// store whose database has no conversation_turn_fts virtual table --
// the FTS5 index ensureFTS5 (search.go) only creates on the SQLite
// dialect (P1 has no Postgres FTS5 equivalent). Search fails closed
// rather than returning an empty or partial result set that looks like
// "no matches".
var ErrSearchUnavailable = cascade.New(cascade.KindUnsupported, "conversation: full-text search unavailable (no fts5 index on this store)")

// ErrThreadNotFound reports that ArchiveThread was called with a thread
// id that has no corresponding row. Unlike AppendSegment's turn-existence
// check (a structural FK gap the append path leans on because SQLite's
// FOREIGN KEY constraints are never enabled -- no PRAGMA foreign_keys=ON
// anywhere in this tree), ArchiveThread checks explicitly rather than
// silently writing an orphaned archive-marker row.
var ErrThreadNotFound = cascade.New(cascade.KindNotFound, "conversation: thread not found")
