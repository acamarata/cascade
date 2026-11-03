// Package sessions is the fleet sessions storage domain
// (02-TARGET-STRUCTURE.md §internal/fleet/): the SessionRecord schema and
// the Get/List/Upsert/TransitionState/Touch operations S-24.T1's process
// census, S-24.T4's hook handler, and S-25.T2's `cascade fleet sessions`
// surface all read and write through.
//
// Purpose: persist one row per harness process (session_id, harness,
//
//	account, pid, an opaque state string, timestamps, and the R-16.48
//	activity columns) and emit fleet.sessions.changed on every successful
//	write.
//
// Inputs: a pkg/provider.Store already scoped to the sessions domain (see
//
//	the CONTRACT DEVIATION note below) and an injected runtime.Clock.
//
// Outputs: SessionRecord values, or a pkg/cascade taxonomy error.
// Constraints: this package hardcodes NO state names and NO transition
//
//	graph — TransitionState is a plain compare-and-swap on the State
//	field, and the vocabulary/graph both belong to S-25.T1's corpus-
//	derived SessionState enum (R-14.41). Deterministic: every timestamp
//	comes from the injected Clock, never time.Now.
//
// CONTRACT DEVIATION (domain registration, recorded, not papered over).
// The contract text describes "the sessions storage domain in cascade.db"
// as if this ticket adds a DomainID. R-14.5's twelve-domain set is CLOSED
// (R-16.51 and R-16.75 are its two ratified amendments, adding `policy`
// and `ci_results`) and this ticket has no standing to add a thirteenth.
// internal/storage/domains.go
// already documents DomainSessions's OwnerPkg as "internal/fleet
// (sessions, nodes, lanes, journal — P1-E13-W3-S27-T1)" — sessions is
// literally named first. This package therefore persists under
// DefaultNamespace = string(storage.DomainSessions), the SAME existing
// domain internal/fleet/journal already uses, distinguished from
// journal's "e:"/"h:" keys by this package's own "session:" key prefix.
// This mirrors internal/fleet/journal/store.go's own CONTRACT DEVIATION
// note for the identical situation, and internal/audit/record.go's
// precedent before it.
//
// SPORT: internal.fleet.sessions.Store/ADDED (P1-E12-W3-S24-T3).
package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// DefaultNamespace is the provider.Store namespace SessionRecords persist
// under. See this file's CONTRACT DEVIATION note above.
const DefaultNamespace = string(storage.DomainSessions)

// sessionKeyPrefix namespaces this package's keys apart from
// internal/fleet/journal's "e:"/"h:" keys within the shared DomainSessions
// namespace.
const sessionKeyPrefix = "session:"

// changedNamespace and changedKind are R-21.272's ratified single
// namespace: topic "fleet.sessions", event "fleet.sessions.changed".
const (
	changedNamespace = "fleet.sessions"
	changedKind      = events.EventKind("fleet.sessions.changed")
)

// writeMu serialises every read-modify-write in this package, across all
// Store values, so a concurrent write can never drop a taint. Taken once
// per write and never re-entered.
var writeMu sync.Mutex

func sessionKey(id string) string { return sessionKeyPrefix + id }

// SessionRecord is one row of the sessions domain: session/process
// attribution only (round-17 audit, 18-T0-RULINGS-R16.md) — never node
// hardware/capability data, which is Q/S-36's concern.
type SessionRecord struct {
	SessionID string `json:"session_id"`
	Harness   string `json:"harness"`
	Account   string `json:"account"`
	PID       int    `json:"pid"`
	// State is an OPAQUE value; its vocabulary is owned solely by
	// S-25.T1's corpus-derived SessionState enum (R-14.41). This package
	// stores states, it does not define them.
	State     string `json:"state"`
	StartedAt int64  `json:"started_at"`
	UpdatedAt int64  `json:"updated_at"`

	// R-16.48 activity columns, ratified by R-21.272.
	InstructionsLoadedAt *int64  `json:"instructions_loaded_at,omitempty"`
	LastPromptAt         *int64  `json:"last_prompt_at,omitempty"`
	LastToolAt           *int64  `json:"last_tool_at,omitempty"`
	ToolCount            int64   `json:"tool_count"`
	CompactionCount      int64   `json:"compaction_count"`
	ParentSessionID      *string `json:"parent_session_id,omitempty"`

	// Sticky taint (contract:session-taint). Set only by MarkUntrusted and
	// never cleared; Upsert carries them over from the stored row.
	Untrusted      bool   `json:"untrusted"`
	UntrustedSince *int64 `json:"untrusted_since,omitempty"`
	UntrustedRef   string `json:"untrusted_ref,omitempty"`
}

// Touch field names — the closed R-16.48 activity-column set Touch
// accepts. Declared as a set (not an enum type) since Touch's contract
// names them by their storage column, not by a domain-owned vocabulary.
const (
	FieldInstructionsLoadedAt = "instructions_loaded_at"
	FieldLastPromptAt         = "last_prompt_at"
	FieldLastToolAt           = "last_tool_at"
	FieldToolCount            = "tool_count"
	FieldCompactionCount      = "compaction_count"
)

// Sentinel errors. Each wraps exactly one frozen pkg/cascade.Kind
// (R-14.2): domain-specific sentinels live in their owning package.
var (
	// ErrNotFound is returned when session_id names no record.
	ErrNotFound = cascade.New(cascade.KindNotFound, "sessions: session not found")
	// ErrInvalidTransition is returned when TransitionState's from does
	// not match the record's current State.
	ErrInvalidTransition = cascade.New(cascade.KindConflict, "sessions: invalid state transition")
	// ErrUnknownField is returned when Touch is called with a field name
	// outside the R-16.48 activity set.
	ErrUnknownField = cascade.New(cascade.KindInvalidInput, "sessions: unknown activity field")
	// ErrInvalidRecord is returned for a SessionRecord with an empty
	// SessionID.
	ErrInvalidRecord = cascade.New(cascade.KindInvalidInput, "sessions: invalid record")
)

// EventBus is the minimal seam Store publishes fleet.sessions.changed
// through — duck-typed against *events.Bus's own Publish signature so
// this package never requires a live bus (nil is valid; see Store.emit).
type EventBus interface {
	Publish(ctx context.Context, namespace string, kind events.EventKind, source string, payload []byte) (events.Event, error)
}

// Store is the sessions domain's provider.Store-backed implementation.
// The zero value is not usable; construct with New.
type Store struct {
	store provider.Store
	clock runtime.Clock
	bus   EventBus // optional; nil means "no bus wired yet"
}

// New returns a Store persisting through store (already scoped to the
// sessions domain by the composition root) and stamping every write from
// clock. bus may be nil — emission is best-effort (see emit).
func New(store provider.Store, clock runtime.Clock, bus EventBus) *Store {
	return &Store{store: store, clock: clock, bus: bus}
}

// Get returns the record for id, or ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (SessionRecord, error) {
	if id == "" {
		return SessionRecord{}, cascade.Wrap(cascade.KindInvalidInput, ErrInvalidRecord, "sessions: Get requires a session id")
	}
	return s.getLocked(ctx, id)
}

func (s *Store) getLocked(ctx context.Context, id string) (SessionRecord, error) {
	data, err := s.store.Get(ctx, DefaultNamespace, sessionKey(id))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return SessionRecord{}, cascade.Wrapf(cascade.KindNotFound, ErrNotFound, "session %q", id)
		}
		return SessionRecord{}, cascade.Wrapf(cascade.KindUnavailable, err, "sessions: reading session %q", id)
	}
	var rec SessionRecord
	if uerr := json.Unmarshal(data, &rec); uerr != nil {
		return SessionRecord{}, cascade.Wrapf(cascade.KindIntegrity, uerr, "sessions: decoding session %q", id)
	}
	return rec, nil
}

// Upsert creates or overwrites rec's row, stamps UpdatedAt from the
// injected clock, and emits fleet.sessions.changed on success. The three
// Untrusted fields always come from the stored row when one exists, so an
// Upsert can never clear a taint (P1-HRN-08 R22 B-1).
func (s *Store) Upsert(ctx context.Context, rec SessionRecord) error {
	if rec.SessionID == "" {
		return cascade.Wrap(cascade.KindInvalidInput, ErrInvalidRecord, "sessions: Upsert requires a session id")
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	cur, err := s.getLocked(ctx, rec.SessionID)
	switch {
	case err == nil:
		rec.Untrusted, rec.UntrustedSince, rec.UntrustedRef = cur.Untrusted, cur.UntrustedSince, cur.UntrustedRef
	case !cascade.HasKind(err, cascade.KindNotFound):
		return err
	}
	return s.putLocked(ctx, rec)
}

// putLocked stamps UpdatedAt, writes rec and emits the change event. The
// caller must hold writeMu.
func (s *Store) putLocked(ctx context.Context, rec SessionRecord) error {
	rec.UpdatedAt = s.clock.Now().UnixMilli()
	data, err := json.Marshal(rec)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "sessions: encoding record")
	}
	if err := s.store.Put(ctx, DefaultNamespace, sessionKey(rec.SessionID), data); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "sessions: writing session %q", rec.SessionID)
	}
	s.emit(ctx, rec)
	return nil
}

// TransitionState performs a compare-and-swap on id's State field: it
// succeeds only when the record's current State equals from, then sets
// State to to (idempotent when from == to and the current state already
// matches). It hardcodes no transition graph — validity is exactly
// "current state equals from" (R-14.41: the graph belongs to S-25.T1).
func (s *Store) TransitionState(ctx context.Context, id, from, to string) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	rec, err := s.getLocked(ctx, id)
	if err != nil {
		return err
	}
	if rec.State != from {
		return cascade.Wrapf(cascade.KindConflict, ErrInvalidTransition, "session %q: current state %q != from %q", id, rec.State, from)
	}
	rec.State = to
	return s.putLocked(ctx, rec)
}

// Touch sets or increments exactly one R-16.48 activity column on an
// existing record: the three *_at fields are stamped from the injected
// clock, and ToolCount/CompactionCount are incremented by one. Returns
// ErrNotFound for an unknown session_id and ErrUnknownField for any name
// outside the closed FieldXxx set.
func (s *Store) Touch(ctx context.Context, id, field string) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	rec, err := s.getLocked(ctx, id)
	if err != nil {
		return err
	}
	now := s.clock.Now().UnixMilli()
	switch field {
	case FieldInstructionsLoadedAt:
		rec.InstructionsLoadedAt = &now
	case FieldLastPromptAt:
		rec.LastPromptAt = &now
	case FieldLastToolAt:
		rec.LastToolAt = &now
	case FieldToolCount:
		rec.ToolCount++
	case FieldCompactionCount:
		rec.CompactionCount++
	default:
		return cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownField, "field %q", field)
	}
	return s.putLocked(ctx, rec)
}

// emit best-effort publishes fleet.sessions.changed for rec. A nil bus
// (production always wires one at daemon startup; tests that do not set
// one up leave it nil) or a Publish failure are both swallowed here —
// storage correctness never depends on the event bus, only observability
// does.
func (s *Store) emit(ctx context.Context, rec SessionRecord) {
	if s.bus == nil {
		return
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_, _ = s.bus.Publish(ctx, changedNamespace, changedKind, fmt.Sprintf("sessions:%s", rec.SessionID), payload)
}
