// Package resume is the fleet sub-package (02-TARGET-STRUCTURE.md
// §internal/fleet; resume is a fleet/governor concern per
// 01-FEATURE-INVENTORY row 35 "CORE (governor + resume)") that runs at
// daemon startup, before the daemon accepts new requests, and closes two
// recovery paths over the S-27.T1 journal domain:
//
//  1. Kill -9 / crash recovery: a scan over every entity in the journal
//     finds cursors an unclean process exit left open, classifies each as
//     resumable, terminal, or unknown-outcome, and re-submits every
//     resumable fan-out cursor through K/S-23.T2's FanOut primitive with
//     already-completed legs skipped (R-21.214).
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
// package's own submit.go are, together, the first). This package is
// therefore free to define, and does define, the wire shape of every
// payload it reads or writes:
//
//   - intentPayload (KindIntent/KindAck): {action_id, idempotent, task_id}.
//     ActionID is the stable id R-21.221 requires across every attempt of
//     the same logical action; Idempotent is the caller's own declaration
//     of whether an unacknowledged instance of this action is safe to
//     auto-replay.
//   - resumeCursorPayload (KindResumeCursor): {task_id, legs, request}.
//     Request is the caller's provider.ModelRequest, marshaled verbatim,
//     the minimum needed to re-submit (no field beyond what re-submission
//     requires, per the ticket's "no scope creep" instruction).
//   - legPayload (KindFanOutLegStarted/KindFanOutLegDone): {leg_index,
//     job_id, attempt}. Attempt is R-21.221's fencing attempt number.
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
