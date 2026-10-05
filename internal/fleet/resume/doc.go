// Package resume is the fleet sub-package (02-TARGET-STRUCTURE.md
// §internal/fleet; resume is a fleet/governor concern per
// 01-FEATURE-INVENTORY row 35 "CORE (governor + resume)") that runs at
// daemon startup, before the daemon accepts new requests, and closes two
// recovery paths over the S-27.T1 journal domain:
//
//  1. Kill -9 / crash recovery: a scan over every entity in the journal
//     finds cursors an unclean process exit left open and classifies each
//     as resumable, terminal, or unknown-outcome. A resumable idempotent
//     intent is re-queued. A fan-out cursor is never re-dispatched here
//     (EPIC Decision 12): Scan classifies it, Sweep expires it after the
//     record ttl, and its client re-attaches through conductor.execute,
//     which replays completed legs and runs only the missing ones
//     (R-21.214).
//  2. Upgrade-in-place resume (§D-2): the same scan, run again after
//     D/S-07.T5's drain+exec-relaunch restarts the daemon at a new
//     version, closing that ticket's deferred allowed-fail leg.
//
// # Journal payload contracts this package owns
//
// The journal domain's Entry.Payload is caller-defined json.RawMessage
// (journal.go); no kind in the closed eight-member enum prescribes a
// shape. At the time this package was written, no other production
// caller writes KindIntent, KindResumeCursor, KindFanOutLegStarted or
// KindFanOutLegDone entries (internal/conductor's FanOut and this
// package's own leg appender are, together, the first). This package is
// therefore free to define, and does define, the wire shape of every
// payload it reads or writes:
//
//   - intentPayload (KindIntent/KindAck): {action_id, idempotent, task_id}.
//     ActionID is the stable id R-21.221 requires across every attempt of
//     the same logical action; Idempotent is the caller's own declaration
//     of whether an unacknowledged instance of this action is safe to
//     auto-replay.
//   - resumeCursorPayload (KindResumeCursor): {fanout_id, task_id, legs,
//     request_digest, request_key}: ids, the leg count and the request
//     record's digest and key, never request content (the request lives
//     only in its record, cursor_write.go).
//   - legPayload (KindFanOutLegStarted/KindFanOutLegDone): {leg_index,
//     job_id, attempt, outcome, result_key, request_digest}. Attempt is the
//     leg's durable start slot (at most three per leg).
//
// # Fail-closed classification
//
// A cursor is Resumable only when every fact needed to resume it safely is
// present and recognized: a resumeCursorPayload naming its leg count, or an
// intentPayload declaring Idempotent with no matching Ack. Anything else —
// a decode failure, a checksum failure, an entity whose journal entries do
// not resolve to one of the two known resumable shapes, or an
// unacknowledged intent that is not declared idempotent — is Terminal or
// UnknownOutcome, never Resumable. See scan.go's classify for the decision
// table.
//
// SPORT: internal.fleet.resume.ResumeManager/ADDED (P1-E13-W3-S27-T2).
package resume
