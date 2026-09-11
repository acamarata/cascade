// Package pews (lifecycle.go): the claim/step/CR/QA/done ticket lifecycle
// state machine (P1-E14-W3-S30-T1) over the S-28 ticket contracts. Every
// transition is recorded through this file's own entity-journal
// capability so state is always replayed, never stored as free text.
//
// Inputs: a *Tree (store.go), a JournalStore (this file), a ticket id, a
// caller operation id; CR/QA also take one of the ticket's own declared
// CRLevel/QALevel tokens (schema.go) — no new level is invented here.
// Outputs: the appended JournalEntry, or a *cascade.Error. An unknown
// event, an unparseable replayed state, or an illegal transition all
// refuse with a typed error, never coerced to a nearby legal state.
// Constraints: no bare time.Now/rand; plugins/** import pkg/** only, never
// internal/** (Art.10.2) — JournalStore is declared locally for that.
//
// Status-field contradiction inherited from S-28.T1/S-29.T3 (both quoted
// in this ticket's journal): the 17+5-field Ticket contract is closed and
// fails closed on an unknown key, so lifecycle state is never written
// onto it — it lives in the journal below, derived by replay, the same
// way residue.go generalized a binary tombstoned/not signal.
//
// SPORT: plugins/pbd/internal/pews lifecycle (ADD) — P1-E14-W3-S30-T1.
package pews

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// LifecycleState is one of six ratified PEWS ticket lifecycle states, in
// transition order. The zero value is deliberately not a member.
type LifecycleState string

// The six states, in transition order.
const (
	StateUnclaimed LifecycleState = "unclaimed"
	StateClaimed   LifecycleState = "claimed"
	StateStep      LifecycleState = "step"
	StateCR        LifecycleState = "cr"
	StateQA        LifecycleState = "qa"
	StateDone      LifecycleState = "done"
)

var lifecycleStates = []LifecycleState{StateUnclaimed, StateClaimed, StateStep, StateCR, StateQA, StateDone}

// Valid reports whether s is a member of the closed six-state machine.
func (s LifecycleState) Valid() bool { return inSet(s, lifecycleStates) }

// LifecycleEvent is one of five ratified lifecycle transitions a caller
// may request. The zero value is deliberately not a member.
type LifecycleEvent string

// The five events a caller may request.
const (
	EventClaim LifecycleEvent = "claim"
	EventStep  LifecycleEvent = "step"
	EventCR    LifecycleEvent = "cr"
	EventQA    LifecycleEvent = "qa"
	EventDone  LifecycleEvent = "done"
)

var lifecycleEvents = []LifecycleEvent{EventClaim, EventStep, EventCR, EventQA, EventDone}

// Valid reports whether e is a member of the closed five-event set.
func (e LifecycleEvent) Valid() bool { return inSet(e, lifecycleEvents) }

// ParseLifecycleEvent parses s as a LifecycleEvent, refusing (never
// coercing) an unknown or empty string with KindInvalidInput.
func ParseLifecycleEvent(s string) (LifecycleEvent, error) {
	e := LifecycleEvent(s)
	if !e.Valid() {
		return "", cascade.Newf(cascade.KindInvalidInput, "pews: unknown lifecycle event %q", s)
	}
	return e, nil
}

// nextLifecycleState is the transition table, asserted against the spec
// by lifecycle_test.go, never a second copy of itself. Exhaustive over
// both closed enums (exhaustive linter). An unlisted pair refuses with
// KindConflict rather than coercing to the nearest legal state.
func nextLifecycleState(current LifecycleState, event LifecycleEvent) (LifecycleState, error) {
	switch current {
	case StateUnclaimed:
		if event == EventClaim {
			return StateClaimed, nil
		}
	case StateClaimed:
		if event == EventStep {
			return StateStep, nil
		}
	case StateStep:
		switch event {
		case EventStep:
			return StateStep, nil
		case EventCR:
			return StateCR, nil
		case EventClaim, EventQA, EventDone:
		}
	case StateCR:
		switch event {
		case EventCR:
			return StateCR, nil
		case EventQA:
			return StateQA, nil
		case EventClaim, EventStep, EventDone:
		}
	case StateQA:
		switch event {
		case EventQA:
			return StateQA, nil
		case EventDone:
			return StateDone, nil
		case EventClaim, EventStep, EventCR:
		}
	case StateDone:
		// Terminal: every event refuses.
	}
	return "", cascade.Newf(cascade.KindConflict, "pews: lifecycle event %q is not valid from state %q", event, current)
}

// JournalEntry is one sealed lifecycle transition record, replayed to
// derive a ticket's LifecycleState. Mirrors Epic M's internal/fleet/
// journal.Entry field-for-field but is declared locally: journal.Kind is
// a named type, so a method taking it cannot satisfy a plugins/**
// interface without importing internal/** (Art.10.2). FileJournalStore
// (plugins/pbd/lifecycle.go) is JournalStore's shipped implementation.
type JournalEntry struct {
	EntityID    string
	Seq         uint64
	Event       LifecycleEvent
	OperationID string
	Payload     json.RawMessage
	TSUnixNano  int64
}

// Time returns the instant this entry was appended.
func (e JournalEntry) Time() time.Time { return time.Unix(0, e.TSUnixNano).UTC() }

// JournalStore is the local, duck-typed seam onto this entity-journal
// capability; Replay returns entityID's entries in Seq order.
type JournalStore interface {
	Append(ctx context.Context, entityID string, event LifecycleEvent, operationID string, payload json.RawMessage) (JournalEntry, error)
	Replay(ctx context.Context, entityID string) ([]JournalEntry, error)
}

// CurrentState replays entityID's journal through the transition table.
// Empty replay means StateUnclaimed; an unparseable event or illegal
// transition refuses the whole call rather than skipping/coercing it.
func CurrentState(ctx context.Context, js JournalStore, entityID string) (LifecycleState, error) {
	entries, err := js.Replay(ctx, entityID)
	if err != nil {
		return "", err
	}
	state := StateUnclaimed
	for _, e := range entries {
		if !e.Event.Valid() {
			return "", cascade.Newf(cascade.KindInvalidInput, "pews: entity %q seq %d: unparseable lifecycle event %q", entityID, e.Seq, e.Event)
		}
		next, terr := nextLifecycleState(state, e.Event)
		if terr != nil {
			return "", terr
		}
		state = next
	}
	if !state.Valid() {
		return "", cascade.Newf(cascade.KindInvalidInput, "pews: entity %q: unknown replayed state %q", entityID, state)
	}
	return state, nil
}

// findTicket locates id in tree, refusing (KindNotFound) when absent.
func findTicket(tree *Tree, id string) (TicketRecord, error) {
	for _, r := range tree.Tickets {
		if r.Ticket.ID == id {
			return r, nil
		}
	}
	return TicketRecord{}, cascade.Newf(cascade.KindNotFound, "pews: ticket %q not found in tree", id)
}

// crLevelParts splits a CRLevel's '+'-joined tokens.
func crLevelParts(c CRLevel) []string { return strings.Split(string(c), "+") }

// crLevelDeclares reports whether declared's joined tokens contain level.
func crLevelDeclares(declared CRLevel, level CRLevel) bool {
	return inSet(string(level), crLevelParts(declared))
}

// LifecyclePayload is a journal entry's decoded body: a CR/QA pass's
// declared level (already present on the ticket) plus an optional note.
type LifecyclePayload struct {
	CRLevel CRLevel `json:"cr_level,omitempty"`
	QALevel QALevel `json:"qa_level,omitempty"`
	Note    string  `json:"note,omitempty"`
}

// DecodeLifecyclePayload decodes payload bytes, tolerating nil/empty
// (Claim and Done carry none) as a zero LifecyclePayload.
func DecodeLifecyclePayload(payload json.RawMessage) (LifecyclePayload, error) {
	if len(payload) == 0 {
		return LifecyclePayload{}, nil
	}
	var p LifecyclePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return LifecyclePayload{}, cascade.Wrap(cascade.KindInvalidInput, err, "pews: decoding lifecycle payload")
	}
	return p, nil
}

func encodeLifecyclePayload(p LifecyclePayload) (json.RawMessage, error) {
	if p.CRLevel == "" && p.QALevel == "" && p.Note == "" {
		return nil, nil
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "pews: encoding lifecycle payload")
	}
	return data, nil
}

// applyEvent is Claim/Step/RecordCR/RecordQA/Done's shared core: refuses a
// nil tree, unknown ticket id, or Claim on a draft phase (RequireBuildable,
// draft.go — retiring its prior test-only status, N/S-30's expected
// caller), replays current state, checks the transition, then appends.
func applyEvent(ctx context.Context, tree *Tree, js JournalStore, ticketID, operationID string, event LifecycleEvent, payload json.RawMessage) (JournalEntry, error) {
	if tree == nil {
		return JournalEntry{}, cascade.New(cascade.KindInvalidInput, "pews: cannot apply a lifecycle event to a nil tree")
	}
	if _, err := findTicket(tree, ticketID); err != nil {
		return JournalEntry{}, err
	}
	if event == EventClaim {
		if err := RequireBuildable(tree); err != nil {
			return JournalEntry{}, err
		}
	}
	current, err := CurrentState(ctx, js, ticketID)
	if err != nil {
		return JournalEntry{}, err
	}
	if _, terr := nextLifecycleState(current, event); terr != nil {
		return JournalEntry{}, terr
	}
	return js.Append(ctx, ticketID, event, operationID, payload)
}

// Claim records StateUnclaimed -> StateClaimed for ticketID.
func Claim(ctx context.Context, tree *Tree, js JournalStore, ticketID, operationID string) (JournalEntry, error) {
	return applyEvent(ctx, tree, js, ticketID, operationID, EventClaim, nil)
}

// Step records a step-in-progress transition (-> StateStep); note is optional free text.
func Step(ctx context.Context, tree *Tree, js JournalStore, ticketID, operationID, note string) (JournalEntry, error) {
	payload, err := encodeLifecyclePayload(LifecyclePayload{Note: note})
	if err != nil {
		return JournalEntry{}, err
	}
	return applyEvent(ctx, tree, js, ticketID, operationID, EventStep, payload)
}

// RecordCR records a CR pass (-> StateCR); level must be one of ticket's declared CRLevel tokens.
func RecordCR(ctx context.Context, tree *Tree, js JournalStore, ticketID, operationID string, level CRLevel) (JournalEntry, error) {
	rec, err := findTicket(tree, ticketID)
	if err != nil {
		return JournalEntry{}, err
	}
	if !level.Valid() || !crLevelDeclares(rec.Ticket.CRLevel, level) {
		return JournalEntry{}, cascade.Newf(cascade.KindInvalidInput, "pews: cr level %q not declared by ticket %q's cr_level %q", level, ticketID, rec.Ticket.CRLevel)
	}
	payload, err := encodeLifecyclePayload(LifecyclePayload{CRLevel: level})
	if err != nil {
		return JournalEntry{}, err
	}
	return applyEvent(ctx, tree, js, ticketID, operationID, EventCR, payload)
}

// RecordQA records a QA pass (-> StateQA); level must equal ticket's declared QALevel exactly.
func RecordQA(ctx context.Context, tree *Tree, js JournalStore, ticketID, operationID string, level QALevel) (JournalEntry, error) {
	rec, err := findTicket(tree, ticketID)
	if err != nil {
		return JournalEntry{}, err
	}
	if !level.Valid() || level != rec.Ticket.QALevel {
		return JournalEntry{}, cascade.Newf(cascade.KindInvalidInput, "pews: qa level %q does not match ticket %q's qa_level %q", level, ticketID, rec.Ticket.QALevel)
	}
	payload, err := encodeLifecyclePayload(LifecyclePayload{QALevel: level})
	if err != nil {
		return JournalEntry{}, err
	}
	return applyEvent(ctx, tree, js, ticketID, operationID, EventQA, payload)
}

// Done records the terminal transition (StateQA -> StateDone).
func Done(ctx context.Context, tree *Tree, js JournalStore, ticketID, operationID string) (JournalEntry, error) {
	return applyEvent(ctx, tree, js, ticketID, operationID, EventDone, nil)
}
