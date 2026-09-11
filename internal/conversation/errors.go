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
