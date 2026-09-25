// Purpose: the read-only classification scan (task 2 of the ticket): for
//   one entity, decide Resumable/Terminal/UnknownOutcome without any side
//   effect, and build the resumeCursor a Resumable entity's re-submission
//   needs.
// Inputs: an entityID and this package's journal.Store.
// Outputs: (*resumeCursor, *AttentionItem, error) — cursor is nil when
//   there is nothing to resume (including "fully completed"); error is
//   nil exactly when classification succeeded, whatever its result.
// Constraints: read-only (Recover's torn-tail repair is the one exception
//   journal.Store itself already performs and documents as recovery, not
//   scan side-effect); fail-closed on anything not explicitly recognized.
// SPORT: internal.fleet.resume.ResumeManager/ADDED (P1-E13-W3-S27-T2).

package resume

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/pkg/provider"
)

// cursorKind discriminates the two Resumable shapes classify recognizes.
type cursorKind uint8

const (
	cursorFanOut cursorKind = iota
	cursorIntent
)

// resumeCursor is the minimum this package needs to re-submit one entity's
// open work. Only the fields matching Kind are meaningful. See doc.go's
// payload-contract section.
type resumeCursor struct {
	TaskID    string
	FanOutID  string // cursorFanOut only: FanOutIDFromEntity of the entity
	Kind      cursorKind
	Request   provider.ModelRequest   // cursorFanOut only
	Legs      int                     // cursorFanOut only
	Completed map[int]conductor.JobID // cursorFanOut only
	ActionID  string                  // cursorIntent only
}

// resumeCursorPayload is the KindResumeCursor wire shape this package
// writes when it observes (or itself would need to describe) a fan-out
// task's resumable state. T discriminates it from fenceMarker, which also
// rides KindResumeCursor (see submit.go); both payloads share the kind
// because R-21.216's enum is closed at eight members and neither shape
// warrants a ninth.
type resumeCursorPayload struct {
	T       string          `json:"t"` // "cursor"
	TaskID  string          `json:"task_id"`
	Legs    int             `json:"legs"`
	Request json.RawMessage `json:"request"`
}

// legPayload is the KindFanOutLegStarted/KindFanOutLegDone wire shape the
// journalAppenderAdapter (submit.go) writes on FanOut's behalf. A done
// entry carries its outcome and, for ok, the result key; the digest binds
// the leg request. It never carries Response content.
type legPayload struct {
	LegIndex      int    `json:"leg_index"`
	JobID         string `json:"job_id,omitempty"`
	Attempt       uint64 `json:"attempt"`
	Outcome       string `json:"outcome,omitempty"`
	ResultKey     string `json:"result_key,omitempty"`
	RequestDigest string `json:"request_digest,omitempty"`
}

// intentPayload is the KindIntent/KindAck wire shape for a generic (non
// fan-out) resumable action. See doc.go.
type intentPayload struct {
	ActionID   string `json:"action_id"`
	Idempotent bool   `json:"idempotent"`
	TaskID     string `json:"task_id"`
}

// scanEntity classifies entityID. A nil cursor with a nil error means
// "nothing to resume" (fully completed, or no recognized open work) — not
// itself a Report.Outcomes entry worth surfacing as terminal/unknown.
func (m *Manager) scanEntity(ctx context.Context, entityID string) (*resumeCursor, *AttentionItem, error) {
	rep, err := m.journal.Recover(ctx, entityID)
	if err != nil {
		return nil, nil, err
	}
	if rep.Truncated > 0 {
		item := &AttentionItem{EntityID: entityID, Reason: "journal tail truncated from seq " + itoa(rep.FirstBadSeq)}
		return nil, item, ErrTruncatedTail
	}

	entries, err := m.journal.Replay(ctx, entityID, journal.Cursor{EntityID: entityID, Seq: 0}, nil)
	if err != nil {
		return nil, nil, err
	}
	cur, attention, err := classify(entries)
	if err != nil || cur == nil || cur.Kind != cursorFanOut {
		return cur, attention, err
	}
	// A fan-out's leg entries live in FanOutEntity(fanoutID); a fan-out
	// cursor in any other entity is not a shape this package resumes.
	id, ok := FanOutIDFromEntity(entityID)
	if !ok {
		return nil, nil, ErrUnrecognizedShape
	}
	cur.FanOutID = id
	return cur, attention, nil
}

// classify implements the decision table doc.go's package comment
// describes: a fan-out resumeCursorPayload wins when present (checked
// first, since a fan-out task may also carry Intent/Ack bookkeeping this
// package does not use); otherwise an unacknowledged Intent decides the
// outcome by its idempotency declaration.
func classify(entries []journal.Entry) (*resumeCursor, *AttentionItem, error) {
	cur, fanOutErr := classifyFanOut(entries)
	if cur != nil || fanOutErr != nil {
		return cur, nil, fanOutErr
	}
	cur, err := classifyIntent(entries)
	return cur, nil, err
}

// classifyFanOut looks for the latest resumeCursorPayload (T=="cursor")
// and, if found, decides Resumable vs. "fully completed" vs.
// ErrUnrecognizedShape (an undecodable cursor payload — fail-closed).
func classifyFanOut(entries []journal.Entry) (*resumeCursor, error) {
	var latest *resumeCursorPayload
	for i := range entries {
		e := &entries[i]
		if e.Kind != journal.KindResumeCursor {
			continue
		}
		var p resumeCursorPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil || p.T != "cursor" {
			continue // not this package's cursor shape (e.g. a fence marker)
		}
		latest = &p
	}
	if latest == nil {
		return nil, nil
	}
	var req provider.ModelRequest
	if err := json.Unmarshal(latest.Request, &req); err != nil {
		return nil, ErrUnrecognizedShape
	}
	completed, err := legOutcomes(entries, latest.Legs)
	if err != nil {
		return nil, err
	}
	if len(completed) >= latest.Legs {
		return nil, nil // every leg already done: nothing to resume
	}
	return &resumeCursor{TaskID: latest.TaskID, Kind: cursorFanOut, Request: req, Legs: latest.Legs, Completed: completed}, nil
}

// legOutcomes decides a fan-out cursor from its leg entries: any
// failed_terminal leg is ErrLegTerminal (terminal); any leg in [0, legs)
// with maxLegStarts starts and no ok done is ErrLegAttemptsExhausted
// (unknown outcome); otherwise it returns the ok legs.
func legOutcomes(entries []journal.Entry, legs int) (map[int]conductor.JobID, error) {
	completed, terminal, err := completedLegs(entries)
	if err != nil {
		return nil, err
	}
	if terminal {
		return nil, ErrLegTerminal
	}
	starts, err := legStartCounts(entries)
	if err != nil {
		return nil, err
	}
	for leg := 0; leg < legs; leg++ {
		if _, ok := completed[leg]; !ok && starts[leg] >= maxLegStarts {
			return nil, ErrLegAttemptsExhausted
		}
	}
	return completed, nil
}

// completedLegs builds the R-21.214 completed map from the
// KindFanOutLegDone entries whose outcome is ok - only those; a failed or
// outcome-less done never counts as completed. terminal reports a
// failed_terminal done. An undecodable leg payload fails closed.
func completedLegs(entries []journal.Entry) (map[int]conductor.JobID, bool, error) {
	out := make(map[int]conductor.JobID)
	terminal := false
	for _, e := range entries {
		if e.Kind != journal.KindFanOutLegDone {
			continue
		}
		var p legPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil, false, ErrUnrecognizedShape
		}
		switch p.Outcome {
		case conductor.LegOutcomeOK:
			out[p.LegIndex] = conductor.JobID(p.JobID)
		case conductor.LegOutcomeFailedTerminal:
			terminal = true
		}
	}
	return out, terminal, nil
}

// legStartCounts counts KindFanOutLegStarted entries per leg index. The
// entries come from Replay, whose (kind, operation_id) dedupe never hides
// a start written by AppendLeg: every start carries its own attempt in
// its operation id. An undecodable start payload fails closed.
func legStartCounts(entries []journal.Entry) (map[int]int, error) {
	counts := make(map[int]int)
	for _, e := range entries {
		if e.Kind != journal.KindFanOutLegStarted {
			continue
		}
		var p legPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil, ErrUnrecognizedShape
		}
		counts[p.LegIndex]++
	}
	return counts, nil
}

// classifyIntent implements the generic (non fan-out) Intent/Ack path. It
// returns (cursor, nil) when the open intent is declared idempotent
// (Resumable, auto re-queue), (nil, ErrAmbiguousOutcome) when it is not
// (UnknownOutcome, held), (nil, ErrUnrecognizedShape) when the payload
// cannot be decoded at all (Terminal, fail-closed), and (nil, nil) when
// there is no open intent.
func classifyIntent(entries []journal.Entry) (*resumeCursor, error) {
	acked := make(map[string]bool)
	for _, e := range entries {
		if e.Kind == journal.KindAck {
			acked[e.OperationID] = true
		}
	}
	var openIntent *journal.Entry
	for i := range entries {
		if entries[i].Kind == journal.KindIntent && !acked[entries[i].OperationID] {
			openIntent = &entries[i]
		}
	}
	if openIntent == nil {
		return nil, nil
	}
	var p intentPayload
	if err := json.Unmarshal(openIntent.Payload, &p); err != nil || p.ActionID == "" {
		return nil, ErrUnrecognizedShape
	}
	if !p.Idempotent {
		return nil, ErrAmbiguousOutcome
	}
	return &resumeCursor{TaskID: p.TaskID, Kind: cursorIntent, ActionID: p.ActionID}, nil
}

// itoa avoids importing strconv solely for one call site in this file's
// doc-facing Reason string (kept trivial and allocation-light on
// purpose).
func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
