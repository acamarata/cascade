// Purpose: fan-out and intent classification. scanEntity (the journal-only
//   Manager's read-only classifier) decides Resumable/Terminal/Unknown for
//   one entity; Scan (contract:fanout-producer) classifies every in-scope
//   fan-out over the daemon store, loading the request from its record.
// Inputs: an entityID and a journal.Store (Manager); FanOutDeps (Scan).
// Outputs: scanEntity: (*resumeCursor, *AttentionItem, error); Scan: one
//   FanOutScan per in-scope fan-out.
// Constraints: Scan dispatches nothing and writes only terminal markers and
//   record deletes; it never touches an entity outside its scope (sweep.go).
//   Fail-closed on anything not explicitly recognized.
// SPORT: internal.fleet.resume.ResumeManager/CHANGE (P1-CORE-19).

package resume

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/pkg/cascade"
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
	FanOutID  string // cursorFanOut only: the cursor fanout_id (= the entity id)
	Kind      cursorKind
	Request   provider.ModelRequest   // cursorFanOut only
	Legs      int                     // cursorFanOut only
	Completed map[int]conductor.JobID // cursorFanOut only
	ActionID  string                  // cursorIntent only
}

// resumeCursorPayload is the KindResumeCursor wire shape of a fan-out's
// cursor, written once at seq 1 of FanOutEntity(fanout_id) by
// FanOutStore.WriteCursor (cursor_write.go). It carries ids, the leg count
// and the request record's digest and key, never request content. T
// discriminates it from fenceMarker, which also rides KindResumeCursor
// (see submit.go); both share the kind because R-21.216's enum is closed
// at eight members and neither shape warrants a ninth.
type resumeCursorPayload struct {
	T             string `json:"t"` // "cursor"
	FanOutID      string `json:"fanout_id"`
	TaskID        string `json:"task_id"`
	Legs          int    `json:"legs"`
	RequestDigest string `json:"request_digest"`
	RequestKey    string `json:"request_key"`
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
	if !ok || cur.FanOutID != id {
		return nil, nil, ErrUnrecognizedShape
	}
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
	if latest.FanOutID == "" || latest.Legs < 1 {
		return nil, ErrUnrecognizedShape
	}
	completed, err := legOutcomes(entries)
	if err != nil {
		return nil, err
	}
	if len(completed) >= latest.Legs {
		return nil, nil // every leg already done: nothing to resume
	}
	// The request content lives only in the request record, which this
	// journal-only Manager cannot read and never needs: it classifies and
	// dispatches nothing. The store-backed Scan loads the record.
	return &resumeCursor{TaskID: latest.TaskID, FanOutID: latest.FanOutID, Kind: cursorFanOut,
		Request: provider.ModelRequest{TaskID: latest.TaskID}, Legs: latest.Legs, Completed: completed}, nil
}

// legOutcomes decides a fan-out cursor from its leg entries: any
// failed_terminal leg is ErrLegTerminal (terminal); otherwise it returns
// the ok legs. A leg at the start cap with no ok done is NOT decided here:
// the journal cannot tell a crash after its LegResult was stored from one
// before, so the cursor stays resumable and the dispatch path decides it -
// conductor.FanOut replays a matching stored record through AuthorizeFn
// (zero provider calls, the missing done appended) and otherwise its start
// is refused with ErrLegAttemptsExhausted (unknown outcome), never sent.
func legOutcomes(entries []journal.Entry) (map[int]conductor.JobID, error) {
	completed, terminal, err := completedLegs(entries)
	if err != nil {
		return nil, err
	}
	if terminal {
		return nil, ErrLegTerminal
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

// Scan classifies every in-scope fan-out (see sweep.go) and dispatches
// nothing: it writes only terminal markers and record deletes. A claimed
// (busy) fan-out is skipped. A nil collaborator is ErrConstructionFailed.
func Scan(ctx context.Context, deps FanOutDeps) ([]FanOutScan, error) {
	js, err := deps.journal()
	if err != nil {
		return nil, err
	}
	entities, err := js.ListEntities(ctx)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "resume: listing journal entities for the scan")
	}
	var out []FanOutScan
	for _, entity := range entities {
		if id, ok := FanOutIDFromEntity(entity); ok {
			if v, keep := scanFanOut(ctx, js, deps, id); keep {
				out = append(out, v)
			}
		}
	}
	return out, nil
}

// scanFanOut classifies one fan-out under its claim. keep is false for a
// busy or foreign entity, which is left untouched.
func scanFanOut(ctx context.Context, js journal.Store, deps FanOutDeps, id string) (FanOutScan, bool) {
	if !deps.Claims.TryClaim(id) {
		return FanOutScan{}, false
	}
	defer deps.Claims.Release(id)
	st, err := loadState(ctx, js, id)
	if err != nil {
		return FanOutScan{FanOutID: id, Class: FanOutTerminal, Err: err}, true
	}
	if !st.Ours {
		return FanOutScan{}, false
	}
	v := FanOutScan{FanOutID: id}
	if st.Final != "" {
		v.Class, v.Err = FanOutFinalMarked, deleteRecords(ctx, js, deps.Store, id)
		return v, true
	}
	req, found, err := getRequest(ctx, deps.Store, id)
	switch {
	case err != nil:
		v.Class, v.Err = FanOutTerminal, err
	case !found || st.Terminal:
		cause := ErrRequestRecordMissing
		if found {
			cause = ErrLegTerminal
		}
		v.Class, v.Err = FanOutTerminal, errors.Join(cause, finalize(ctx, js, deps.Store, id, OutcomeTerminal))
	case len(st.Completed) >= st.Cursor.Legs:
		v.Class, v.Request = FanOutCompleteUndelivered, req
	default:
		v.Class, v.Request = FanOutResumable, req
	}
	return v, true
}
