package journal

// Purpose: the fleet.journal_show / fleet.journal_replay JSON-RPC 2.0
//
//	read-path doors (P1-E13-W3-S27-T4): a server-side HandlerFunc pair
//	RegisterHandlers binds into an *rpc.Registry, plus a typed Client
//	wrapper mirroring internal/fleet/sessions/rpc.go's identical
//	pattern for cmd/cascade/fleet_journal.go's CLI surface.
//
// Inputs: fleet.journal_show/replay's params (entity_id, an optional
//
//	after_seq/from_seq cursor, and, for show, an optional limit).
//
// Outputs: the entity's entries in sequence order, or a pkg/cascade
//
//	taxonomy error.
//
// Constraints: unknown entity_id (never appended to) returns a typed
//
//	KindNotFound error, never an empty array — see Reader/HeadSeq below
//	for how "unknown" is told apart from "known but caught up to the
//	cursor". Server-side --limit is clamped at maxShowLimit; this file
//	never mutates the journal it reads (read-only, matching
//	journal.Store's own Append/Checkpoint/Replay/Close split — this file
//	calls Replay and the new HeadSeq peek only).
//
// CONTRACT DEVIATION (RPC composition-root wiring, recorded, not papered
// over). Every landed precedent for registering a daemon RPC method
// (context.scope.show, memory.*, recall.*, fleet.sessions.list per
// P1-E12-W3-S24-T3) is called from cmd/cascade/daemon_unix_run.go's
// buildRPCServer, which this ticket's files_scope does not include
// (files_scope.change lists only cmd/cascade/fleet.go). RegisterHandlers
// below is exercised end-to-end against a real *rpc.Registry
// (rpc_test.go) and, for TestEpicMAcceptance, against a real daemon this
// package's own test builds by calling RegisterHandlers directly against
// a fresh registry and a real unix socket (internal/integration's own
// established pattern, context_scope_test.go) — never against the
// production buildRPCServer composition root, which this ticket cannot
// edit. See internal/build/testonly-allow.json for the tracked entry
// naming the wiring ticket that must call RegisterHandlers from
// buildRPCServer, exactly as fleet.sessions.list's own entry already
// does for the identical situation.
//
// CONTRACT DEVIATION (replay --stream, recorded, not papered over). The
// ticket's HOW text says `fleet journal replay --stream` "delivers SSE
// events". No SSE topic exists for the journal domain, and wiring one
// (a second GET /events route, or a topic-dispatching rework of the
// single SSEHandler cmd/cascade/daemon_unix_run.go currently mounts) is
// both out of this ticket's files_scope and, per the AGENT-BRIEF's
// REPLAY IS NOT RE-EXECUTION rule, arguably the wrong shape for a
// historical read: unlike fleet.sessions --watch (a genuine live
// subscription to future changes), journal replay reconstructs entries
// that already happened — there is nothing "live" for a journal-changed
// event to report that a second, later RPC call would not already see.
// cmd/cascade/fleet_journal.go's --stream flag therefore renders the
// SAME batched fleet.journal_replay RPC response as one NDJSON line per
// entry (internal/output's existing NDJSONWriter, the same wire format
// fleet sessions --watch's non-TTY path already uses) rather than
// dialing a live event stream. See that file's own doc comment.
//
// SPORT: internal.fleet.journal.RegisterHandlers/ADDED,
//
//	internal.fleet.journal.Client/ADDED (P1-E13-W3-S27-T4).

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// MethodShow is the fleet.journal_show JSON-RPC 2.0 method name.
const MethodShow = "fleet.journal_show"

// MethodReplay is the fleet.journal_replay JSON-RPC 2.0 method name.
const MethodReplay = "fleet.journal_replay"

// maxShowLimit is the server-side clamp on a caller-requested limit,
// applied regardless of what the caller asked for (never an error to the
// caller, per this ticket's acceptance criteria).
const maxShowLimit = 500

// ErrUnknownEntity is returned when entity_id names an entity whose
// journal has never had anything appended to it. Distinct from a known
// entity whose journal is simply caught up to the requested cursor (an
// empty result, no error) — see HeadSeq's doc comment for how the two
// are told apart.
var ErrUnknownEntity = cascade.New(cascade.KindNotFound, "journal: unknown entity")

// ErrCursorBeyondHead is returned when a caller's after_seq/from_seq
// names a sequence number past the entity's current head.
var ErrCursorBeyondHead = cascade.New(cascade.KindInvalidInput, "journal: cursor sequence exceeds entity's log")

// ErrInvalidParams is returned for a request params object this package
// cannot decode: an unrecognized key, or a field whose JSON shape does
// not match (e.g. a negative number where after_seq/from_seq, both
// uint64, are required to be non-negative).
var ErrInvalidParams = cascade.New(cascade.KindInvalidInput, "journal: invalid request params")

// Reader is the read surface fleet.journal_show/replay need: Replay plus
// a lightweight existence check via HeadSeq. Narrower than this
// package's own Store interface (journal.go) so rpc_test.go's stub can
// satisfy it without a real provider.Store behind it. *SQLiteStore
// satisfies Reader via the HeadSeq method below.
type Reader interface {
	// Replay returns entityID's entries after cursor, exactly as
	// Store.Replay documents.
	Replay(ctx context.Context, entityID string, cursor Cursor, kinds []Kind) ([]Entry, error)
	// HeadSeq returns entityID's current head sequence number: 0 with a
	// nil error means entityID has never had anything appended.
	HeadSeq(ctx context.Context, entityID string) (uint64, error)
}

// HeadSeq exposes SQLiteStore's own per-entity head pointer for the
// unknown-entity check above. Declared here (not store.go) since it is a
// pure read over lockFor/recoverEntityLocked/loadHeadFrom, all already
// defined in this package — adding it needs no edit to any existing file
// (files_scope for this ticket adds only this one).
func (s *SQLiteStore) HeadSeq(ctx context.Context, entityID string) (uint64, error) {
	lock := s.lockFor(entityID)
	lock.Lock()
	defer lock.Unlock()
	if _, err := s.recoverEntityLocked(ctx, entityID); err != nil {
		return 0, err
	}
	return s.loadHeadFrom(ctx, s.store, entityID)
}

// Compile-time proof that SQLiteStore satisfies Reader.
var _ Reader = (*SQLiteStore)(nil)

// showParams is fleet.journal_show's wire request shape. Unknown fields
// are rejected (decodeParams), mirroring sessions.listParams.
type showParams struct {
	EntityID string  `json:"entity_id"`
	AfterSeq *uint64 `json:"after_seq,omitempty"`
	Limit    *int    `json:"limit,omitempty"`
}

// showResult is fleet.journal_show's wire response shape.
type showResult struct {
	Entries []Entry `json:"entries"`
}

// replayParams is fleet.journal_replay's wire request shape.
type replayParams struct {
	EntityID string  `json:"entity_id"`
	FromSeq  *uint64 `json:"from_seq,omitempty"`
}

// replayResult is fleet.journal_replay's wire response shape.
type replayResult struct {
	Entries []Entry `json:"entries"`
}

// decodeParams decodes raw into a T, rejecting any key T does not
// declare, matching sessions.decodeListParams's convention. An empty raw
// decodes as T's zero value.
func decodeParams[T any](raw json.RawMessage) (T, error) {
	var p T
	if len(raw) == 0 {
		return p, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidParams, "journal: decoding request params: %v", err)
	}
	return p, nil
}

// resolveEntries is the shared read path fleet.journal_show and
// fleet.journal_replay both use: refuse an unknown entity (typed error,
// never an empty array), refuse a cursor past the head (typed error),
// call Replay, and clamp to at most maxShowLimit entries when limit > 0.
// Never writes to reader: this is the read-only half of the journal
// contract (see this file's package doc comment).
func resolveEntries(ctx context.Context, reader Reader, entityID string, after *uint64, limit int) ([]Entry, error) {
	if entityID == "" {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidEntry, "journal: entity_id is required")
	}
	head, err := reader.HeadSeq(ctx, entityID)
	if err != nil {
		return nil, err
	}
	if head == 0 {
		return nil, cascade.Wrapf(cascade.KindNotFound, ErrUnknownEntity, "journal: entity %q has no journal", entityID)
	}
	var afterSeq uint64
	if after != nil {
		afterSeq = *after
		if afterSeq > head {
			return nil, cascade.Wrapf(cascade.KindInvalidInput, ErrCursorBeyondHead,
				"journal: cursor %d exceeds entity %q head %d", afterSeq, entityID, head)
		}
	}
	entries, err := reader.Replay(ctx, entityID, Cursor{EntityID: entityID, Seq: afterSeq}, nil)
	if err != nil {
		return nil, err
	}
	return clampEntries(entries, limit), nil
}

// clampEntries applies the server-side limit clamp: limit <= 0 means
// unlimited; a limit above maxShowLimit is silently lowered to it (never
// an error to the caller, per this ticket's acceptance criteria).
func clampEntries(entries []Entry, limit int) []Entry {
	if limit <= 0 {
		return entries
	}
	if limit > maxShowLimit {
		limit = maxShowLimit
	}
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries
}

// RegisterHandlers binds fleet.journal_show and fleet.journal_replay to
// registry, backed by reader. See this file's CONTRACT DEVIATION note
// for why the composition-root call site is out of this ticket's
// files_scope.
func RegisterHandlers(registry *rpc.Registry, reader Reader) {
	registry.Register(MethodShow, func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := decodeParams[showParams](raw)
		if err != nil {
			return nil, err
		}
		limit := 0
		if p.Limit != nil {
			limit = *p.Limit
		}
		entries, err := resolveEntries(ctx, reader, p.EntityID, p.AfterSeq, limit)
		if err != nil {
			return nil, err
		}
		return showResult{Entries: entries}, nil
	})
	registry.Register(MethodReplay, func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := decodeParams[replayParams](raw)
		if err != nil {
			return nil, err
		}
		entries, err := resolveEntries(ctx, reader, p.EntityID, p.FromSeq, 0)
		if err != nil {
			return nil, err
		}
		return replayResult{Entries: entries}, nil
	})
}
