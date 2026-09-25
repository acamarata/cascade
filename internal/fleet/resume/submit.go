// Purpose: task 3 of the ticket — re-submission. Dispatches a Resumable
//   cursor's remaining work through K/S-23.T2's FanOut primitive (fan-out
//   cursors) or a generic re-queue Append (intent cursors), fenced by a
//   monotonically increasing attempt number and a stable action id
//   (R-21.221), and never applying a result superseded by a newer
//   concurrent attempt.
// Inputs: a resumeCursor (scan.go) this package's own journal.Store.
// Outputs: legs actually dispatched (0 for a discarded/stale result or a
//   generic re-queue) and a taxonomy error.
// Constraints: fencing here is task-scoped, not per-leg (see this file's
//   doc comment on resubmitFanOut for the recorded deviation from the
//   contract's literal per-{task_id,leg_index} wording); every re-dispatch
//   still carries an attempt number on its own journal entries.
// SPORT: internal.fleet.resume.ResumeManager/ADDED (P1-E13-W3-S27-T2).

package resume

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fenceMarker is the KindResumeCursor wire shape a fencing claim writes
// (T=="fence"), distinct from resumeCursorPayload (T=="cursor", scan.go).
type fenceMarker struct {
	T        string `json:"t"` // "fence"
	ActionID string `json:"action_id"`
}

// resubmit dispatches cursor's remaining work by its Kind.
func (m *Manager) resubmit(ctx context.Context, cursor resumeCursor) (int, error) {
	switch cursor.Kind {
	case cursorFanOut:
		return m.resubmitFanOut(ctx, cursor)
	case cursorIntent:
		return 0, m.resubmitIntent(ctx, cursor)
	default:
		return 0, ErrUnrecognizedShape
	}
}

// resubmitFanOut re-dispatches cursor's unfinished legs through FanOut.
//
// FENCING GRANULARITY DEVIATION (recorded, not papered over): the
// contract's task 3 and clause C ask for a fencing attempt number "for
// its {task_id, leg_index}" — per leg. conductor.FanOut (fanout.go,
// K/S-23.T2) dispatches every leg of one call internally and returns only
// the aggregate result; it has no per-leg cancellation or mid-flight
// result-rejection seam, and threading one through FanOut's signature
// would ripple across every existing caller and test in
// internal/conductor, a package this ticket's files_scope lists for
// resume-side wiring only. This package therefore fences at the
// TASK level: one attempt number covers the whole re-submission call, and
// a newer concurrent resume of the SAME task supersedes every leg of an
// older one together, not leg-by-leg. Each leg's own journal entries carry
// the per-leg attempt contract:fanout-leg-results defines (1 + the leg's
// prior starts, capped at 3; see AppendLeg), not this task-level number.
func (m *Manager) resubmitFanOut(ctx context.Context, cursor resumeCursor) (int, error) {
	actionID := FanOutEntity(cursor.TaskID)
	myAttempt, err := m.claimAttempt(ctx, cursor.TaskID, actionID)
	if err != nil {
		return 0, err
	}

	_, dispatchErr := m.fanOut(ctx, cursor.Request, cursor.Legs, cursor.Completed, m.withPermit, m.legAppender())
	dispatched := cursor.Legs - len(cursor.Completed)
	if dispatchErr != nil {
		return 0, dispatchErr
	}

	latest, err := m.latestAttempt(ctx, cursor.TaskID, actionID)
	if err != nil {
		return dispatched, err
	}
	if latest > myAttempt {
		_ = m.journalDiscard(ctx, cursor.TaskID, actionID, myAttempt, latest)
		return 0, ErrStaleAttempt
	}
	return dispatched, nil
}

// resubmitIntent re-queues a generic idempotent action under a fresh
// attempt: it appends a new KindIntent entry sharing the original
// ActionID (R-21.221's "stable action id ... identical across attempts")
// but a fresh operation id (Append never dedupes on write; Replay's own
// dedupe is by (kind, operation_id), so a genuinely new attempt needs a
// distinct one to be visible as its own entry). No exec seam exists for a
// generic action at this layer (only fan-out has one, via FanOut); the
// re-queue Append IS the observable "auto re-queued" effect this ticket's
// acceptance criterion (TestResumeIdempotentActionsOnlyRequeued) checks.
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

// claimAttempt appends a fencing marker for taskID/actionID and returns
// the sequence number Append allocated. The journal's own per-entity,
// transactionally-serialized Append (store.go's per-entity lock) already
// gives concurrent callers distinct, strictly increasing Seq values for
// the same entity, so Seq itself IS R-21.221's fencing attempt number —
// no second counter is invented.
func (m *Manager) claimAttempt(ctx context.Context, taskID, actionID string) (uint64, error) {
	opID, err := cascade.NewID()
	if err != nil {
		return 0, cascade.Wrap(cascade.KindInternal, err, "resume: minting fence operation id")
	}
	payload, err := json.Marshal(fenceMarker{T: "fence", ActionID: actionID})
	if err != nil {
		return 0, cascade.Wrap(cascade.KindInternal, err, "resume: encoding fence marker")
	}
	e, err := m.journal.Append(ctx, taskID, journal.KindResumeCursor, string(opID), payload)
	if err != nil {
		return 0, err
	}
	return e.Seq, nil
}

// latestAttempt returns the highest Seq any fence marker for
// {taskID, actionID} has reached, including markers claimed after
// myAttempt (a concurrent, newer resume of the same task).
func (m *Manager) latestAttempt(ctx context.Context, taskID, actionID string) (uint64, error) {
	entries, err := m.journal.Replay(ctx, taskID, journal.Cursor{EntityID: taskID, Seq: 0}, []journal.Kind{journal.KindResumeCursor})
	if err != nil {
		return 0, err
	}
	var highest uint64
	for _, e := range entries {
		var fm fenceMarker
		if json.Unmarshal(e.Payload, &fm) != nil || fm.T != "fence" || fm.ActionID != actionID {
			continue
		}
		if e.Seq > highest {
			highest = e.Seq
		}
	}
	return highest, nil
}

// journalDiscard records that myAttempt's result was superseded by
// latest and discarded — R-21.221's "stale-attempt results are journaled
// and discarded, never applied", as an Ack entry so a reader scanning for
// open intents never mistakes the discard for outstanding work.
func (m *Manager) journalDiscard(ctx context.Context, taskID, actionID string, myAttempt, latest uint64) error {
	payload, err := json.Marshal(map[string]uint64{"discarded_attempt": myAttempt, "superseded_by": latest})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "resume: encoding discard record")
	}
	_, err = m.journal.Append(ctx, taskID, journal.KindAck, "discard:"+actionID+":"+itoa(myAttempt), payload)
	return err
}

// legAppender adapts this Manager's journal into conductor.JournalAppender
// for one resubmitFanOut call. It is journal-only: its LegResultStore
// methods refuse (ErrLegStoreUnset), because the Manager holds no
// provider.Store.
func (m *Manager) legAppender() conductor.JournalAppender {
	return &journalAppenderAdapter{journal: m.journal}
}

// AppendLeg writes one leg entry into the journal entity
// FanOutEntity(fanoutID) under operation id
// <fanoutID>#<leg>#<attempt>#<kind>. A start's attempt is 1 + the leg's
// prior starts, refused with ErrLegAttemptsExhausted at the cap; a done
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

// appendStarted counts the leg's prior starts and appends the next one,
// serialized so two starts can never share an attempt (and so an
// operation id), which keeps Replay's (kind, operation_id) dedupe from
// hiding a start.
func (a *journalAppenderAdapter) appendStarted(ctx context.Context, fanoutID string, legIndex int, digest string) (uint64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	entries, err := a.journal.Replay(ctx, FanOutEntity(fanoutID), journal.Cursor{EntityID: FanOutEntity(fanoutID)}, []journal.Kind{journal.KindFanOutLegStarted})
	if err != nil {
		return 0, err
	}
	starts, err := legStartCounts(entries)
	if err != nil {
		return 0, err
	}
	if starts[legIndex] >= maxLegStarts {
		return 0, ErrLegAttemptsExhausted
	}
	p := legPayload{LegIndex: legIndex, Attempt: uint64(starts[legIndex]) + 1, RequestDigest: digest}
	return p.Attempt, a.appendLegEntry(ctx, fanoutID, journal.KindFanOutLegStarted, p)
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
