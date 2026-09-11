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
// older one together, not leg-by-leg. Every individual leg's own journal
// entry still carries that task-level attempt (via fencedAppender below),
// satisfying clause C's "the journal append carries the attempt"
// literally, even though "reject a result whose attempt is older" is
// enforced once per task rather than once per leg.
func (m *Manager) resubmitFanOut(ctx context.Context, cursor resumeCursor) (int, error) {
	actionID := "fanout:" + cursor.TaskID
	myAttempt, err := m.claimAttempt(ctx, cursor.TaskID, actionID)
	if err != nil {
		return 0, err
	}

	_, dispatchErr := m.fanOut(ctx, cursor.Request, cursor.Legs, cursor.Completed, m.withPermit, m.fencedAppender(cursor.TaskID, myAttempt))
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

// fencedAppender adapts this Manager's journal into conductor.JournalAppender
// for one resubmitFanOut call, stamping every leg entry with attempt —
// the first production caller of this seam (K/S-23.T2's FanOut has had
// none until this ticket).
func (m *Manager) fencedAppender(taskID string, attempt uint64) conductor.JournalAppender {
	return journalAppenderAdapter{journal: m.journal, taskID: taskID, attempt: attempt}
}

// journalAppenderAdapter implements conductor.JournalAppender by
// translating FanOut's (kind string, taskID, legIndex, fields) call shape
// into a real journal.Append with this package's legPayload.
type journalAppenderAdapter struct {
	journal journal.Store
	taskID  string
	attempt uint64
}

func (a journalAppenderAdapter) AppendLeg(ctx context.Context, kind string, taskID string, legIndex int, fields map[string]string) error {
	k, ok := legKind(kind)
	if !ok {
		return cascade.Newf(cascade.KindInvalidInput, "resume: unrecognized fan-out leg journal kind %q", kind)
	}
	payload, err := json.Marshal(legPayload{LegIndex: legIndex, JobID: fields["job_id"], Attempt: a.attempt})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "resume: encoding leg payload")
	}
	opID := taskID + "#" + itoa(uint64(legIndex)) + "#" + itoa(a.attempt) + "#" + string(k)
	_, err = a.journal.Append(ctx, taskID, k, opID, payload)
	return err
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
