// Purpose: task 3 of the ticket, narrowed by EPIC Decision 12: a resumable
//   idempotent intent is re-queued under a fresh operation id; a resumable
//   fan-out cursor is never re-dispatched here (its client re-attaches
//   through conductor.execute). Also the leg journal appender every
//   fan-out writer shares (AppendLeg and its payload rules).
// Inputs: a resumeCursor (scan.go) and this package's own journal.Store.
// Outputs: a taxonomy error from the re-queue append; leg attempts.
// Constraints: nothing in this file dispatches a provider call.
// SPORT: internal.fleet.resume.ResumeManager/CHANGE (P1-CORE-15).

package resume

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fenceMarker is the KindResumeCursor wire shape of a fencing claim
// (T=="fence"), distinct from resumeCursorPayload (T=="cursor", scan.go).
// Nothing writes one any more; journals written before fan-out re-dispatch
// was removed can still hold them, and every reader skips them.
type fenceMarker struct {
	T        string `json:"t"` // "fence"
	ActionID string `json:"action_id"`
}

// requeue re-queues a resumable intent cursor and leaves a fan-out cursor
// alone: classification is the whole of fan-out resume (EPIC Decision 12).
func (m *Manager) requeue(ctx context.Context, cursor resumeCursor) error {
	switch cursor.Kind {
	case cursorFanOut:
		return nil
	case cursorIntent:
		return m.resubmitIntent(ctx, cursor)
	default:
		return ErrUnrecognizedShape
	}
}

// resubmitIntent re-queues a generic idempotent action under a fresh
// attempt: it appends a new KindIntent entry sharing the original
// ActionID (R-21.221's "stable action id ... identical across attempts")
// but a fresh operation id (Append never dedupes on write; Replay's own
// dedupe is by (kind, operation_id), so a genuinely new attempt needs a
// distinct one to be visible as its own entry). No exec seam exists for a
// generic action at this layer; the re-queue Append IS the observable
// "auto re-queued" effect this ticket's acceptance criterion
// (TestResumeIdempotentActionsOnlyRequeued) checks.
func (m *Manager) resubmitIntent(ctx context.Context, cursor resumeCursor) error {
	opID, err := cascade.NewID()
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "resume: minting re-queue operation id")
	}
	payload, err := json.Marshal(intentPayload{ActionID: cursor.ActionID, Idempotent: true, TaskID: cursor.TaskID})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "resume: encoding re-queue payload")
	}
	_, err = m.journal.Append(ctx, cursor.TaskID, journal.KindIntent, string(opID), payload)
	return err
}

// AppendLeg writes one leg entry into the journal entity
// FanOutEntity(fanoutID) under operation id
// <fanoutID>#<leg>#<attempt>#<kind>. A start's attempt is the durable slot
// claimAttempt allocates, refused with ErrLegAttemptsExhausted at the cap; a done
// entry closes the attempt fields["attempt"] names and must carry a known
// outcome (an ok outcome also its canonical result_key).
func (a *journalAppenderAdapter) AppendLeg(ctx context.Context, kind string, fanoutID string, legIndex int, fields map[string]string) (uint64, error) {
	k, ok := legKind(kind)
	if !ok {
		return 0, cascade.Newf(cascade.KindInvalidInput, "resume: unrecognized fan-out leg journal kind %q", kind)
	}
	if !validFanOutID(fanoutID) || legIndex < 0 {
		return 0, cascade.Newf(cascade.KindInvalidInput, "resume: invalid fan-out leg %q#%d", fanoutID, legIndex)
	}
	if k == journal.KindFanOutLegStarted {
		return a.appendStarted(ctx, fanoutID, legIndex, fields["request_digest"])
	}
	p, err := donePayload(fanoutID, legIndex, fields)
	if err != nil {
		return 0, err
	}
	return p.Attempt, a.appendLegEntry(ctx, fanoutID, k, p)
}

// appendStarted claims the leg's next durable attempt slot, then appends
// the start under it. Slots are unique across every adapter over the same
// store, so two starts never share an attempt (or an operation id), and
// Replay's (kind, operation_id) dedupe never hides a start. A crash after
// the claim and before the append still spends the slot: the cap counts
// raw starts, never fewer.
func (a *journalAppenderAdapter) appendStarted(ctx context.Context, fanoutID string, legIndex int, digest string) (uint64, error) {
	attempt, err := a.claimAttempt(ctx, fanoutID, legIndex, digest)
	if err != nil {
		return 0, err
	}
	p := legPayload{LegIndex: legIndex, Attempt: attempt, RequestDigest: digest}
	return attempt, a.appendLegEntry(ctx, fanoutID, journal.KindFanOutLegStarted, p)
}

// appendLegEntry encodes p and appends it under its operation id.
func (a *journalAppenderAdapter) appendLegEntry(ctx context.Context, fanoutID string, k journal.Kind, p legPayload) error {
	payload, err := json.Marshal(p)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "resume: encoding leg payload")
	}
	opID := fanoutID + "#" + itoa(uint64(p.LegIndex)) + "#" + itoa(p.Attempt) + "#" + string(k)
	_, err = a.journal.Append(ctx, FanOutEntity(fanoutID), k, opID, payload)
	return err
}

// donePayload validates and builds a done entry's payload.
func donePayload(fanoutID string, legIndex int, fields map[string]string) (legPayload, error) {
	attempt, err := strconv.ParseUint(fields["attempt"], 10, 64)
	if err != nil || attempt == 0 {
		return legPayload{}, cascade.Newf(cascade.KindInvalidInput, "resume: fan-out leg done entry needs attempt >= 1, got %q", fields["attempt"])
	}
	p := legPayload{LegIndex: legIndex, JobID: fields["job_id"], Attempt: attempt, Outcome: fields["outcome"],
		ResultKey: fields["result_key"], RequestDigest: fields["request_digest"]}
	switch p.Outcome {
	case conductor.LegOutcomeOK:
		if p.ResultKey != conductor.LegResultKey(fanoutID, legIndex) {
			return legPayload{}, cascade.Newf(cascade.KindInvalidInput, "resume: ok leg done entry has result_key %q, want %q", p.ResultKey, conductor.LegResultKey(fanoutID, legIndex))
		}
	case conductor.LegOutcomeFailedTerminal, conductor.LegOutcomeFailedRetryable:
		if p.ResultKey != "" {
			return legPayload{}, cascade.New(cascade.KindInvalidInput, "resume: a failed leg done entry must not name a result")
		}
	default:
		return legPayload{}, cascade.Newf(cascade.KindInvalidInput, "resume: unrecognized fan-out leg outcome %q", p.Outcome)
	}
	return p, nil
}

// legKind maps FanOut's string kind constants (fanout.go's own literal
// "fanout_leg_started"/"fanout_leg_done") onto the journal's closed Kind
// enum.
func legKind(kind string) (journal.Kind, bool) {
	switch kind {
	case "fanout_leg_started":
		return journal.KindFanOutLegStarted, true
	case "fanout_leg_done":
		return journal.KindFanOutLegDone, true
	default:
		return 0, false
	}
}
