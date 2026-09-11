package conversation

// Purpose (this file): the D/S-06.T3 JSON-RPC method registry surface
//   over T1's Store -- chat.append_turn, chat.get_thread, and
//   chat.list_threads (the chat.* namespace 07-CLI-COMMAND-TREE.md pins
//   for the chat surface). Mirrors internal/fleet/sessions/rpc.go's
//   RegisterHandlers precedent: a generic *rpc.Registry.Register call per
//   method, no method-specific code in internal/rpc itself.
// Inputs: a Store, an EventBus (nil in embedded mode), a Substitutor
//   (nil is a documented no-op passthrough, see sse.go), a Clock, and a
//   mode string ("" for daemon, ModeEmbedded for Windows tier-2).
// Outputs: registered handlers returning wire result shapes, or a
//   pkg/cascade taxonomy error mapped to the JSON-RPC wire format by
//   internal/rpc's existing generic errorObjectFrom/cascade.NewRPCError
//   path (see adapter_errors.go for why this ticket adds no second
//   mapping layer).
// Constraints: L2 workspace-mutation risk (no elevation middleware
//   required -- these three methods are registered directly, matching
//   fleet.sessions.list's own no-elevation precedent). CLIENT-LOCAL ECHO:
//   handleAppendTurn calls emitTurnAppended (sse.go) AFTER the store
//   commit and BEFORE returning its JSON-RPC result, and propagates a
//   non-embedded-mode SSE failure to the caller rather than reporting a
//   turn as appended when no client will ever see it echoed.
//
// CONTRACT DEVIATION (composition-root wiring, recorded, not papered
// over), matching internal/fleet/sessions/rpc.go's identical precedent:
// the actual call to RegisterHandlers from the daemon's buildRPCServer
// (cmd/cascade/daemon_unix_run.go) is outside this ticket's files_scope,
// which lists no cmd/cascade file to change. Recorded in
// internal/build/testonly-allow.json naming this ticket, exactly as
// R-21.273's precedent directs when wiring the composition root itself
// is genuinely out of scope; RegisterHandlers is ready for that call
// site today.
//
// SPORT: internal.conversation.adapter/ADDED (P1-E20-W5-S43-T2).

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The chat.* JSON-RPC 2.0 method names this adapter registers.
const (
	MethodAppendTurn  = "chat.append_turn"
	MethodGetThread   = "chat.get_thread"
	MethodListThreads = "chat.list_threads"
)

// Adapter binds T1's Store to the D/S-06.T3 method registry and the SSE
// mirror. The zero value is not usable; construct with NewAdapter.
type Adapter struct {
	store   Store
	bus     EventBus
	subst   Substitutor
	clock   Clock
	mode    string
	journal JournalStore // optional; nil means "journal.go's write path is not wired here" -- see SetJournal
}

// NewAdapter returns an Adapter dispatching to store, mirroring appended
// turns through bus after passing them through subst, stamping
// CreatedAt/computed ids from clock. bus/subst may be nil (embedded mode,
// or a caller that has not wired substitution yet -- see sse.go's own nil
// handling); mode should be ModeEmbedded on Windows tier-2 and "" (or any
// other value) everywhere else.
func NewAdapter(store Store, bus EventBus, subst Substitutor, clock Clock, mode string) *Adapter {
	return &Adapter{store: store, bus: bus, subst: subst, clock: clock, mode: mode}
}

// SetJournal wires js (P1-E20-W5-S44-T4's entity journal) into
// handleAppendTurn: once set, every chat.append_turn commit goes through
// AppendTurnJournaled instead of calling Store directly. A nil js (the
// default after NewAdapter) preserves T2's original no-journal path.
func (a *Adapter) SetJournal(js JournalStore) {
	a.journal = js
}

// RegisterHandlers binds this adapter's three methods onto registry. See
// this file's CONTRACT DEVIATION note for the composition-root call site.
func (a *Adapter) RegisterHandlers(registry *rpc.Registry) {
	registry.Register(MethodAppendTurn, mapped(a.handleAppendTurn))
	registry.Register(MethodGetThread, mapped(a.handleGetThread))
	registry.Register(MethodListThreads, mapped(a.handleListThreads))
}

// mapped routes h's returned error through adapter_errors.go's
// mapAdapterError before it reaches internal/rpc.Registry.Dispatch's own
// cascade.Kind->JSON-RPC-code mapping -- see that file's CONTRACT
// DEVIATION note for why this is an identity pass-through, not a second
// mapping table.
func mapped(h rpc.HandlerFunc) rpc.HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		result, err := h(ctx, raw)
		if err != nil {
			return nil, mapAdapterError(err)
		}
		return result, nil
	}
}

// appendTurnParams is chat.append_turn's wire request shape: one turn's
// role plus its ordered content segments. Unknown fields are rejected
// (DisallowUnknownFields) so a caller typo surfaces immediately rather
// than being silently dropped.
type appendTurnParams struct {
	ThreadID string              `json:"thread_id"`
	Role     string              `json:"role"`
	Segments []appendSegmentWire `json:"segments"`
}

type appendSegmentWire struct {
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

// appendTurnResult is chat.append_turn's wire response shape.
type appendTurnResult struct {
	ThreadID string `json:"thread_id"`
	TurnID   string `json:"turn_id"`
	Seq      int64  `json:"seq"`
}

func decodeAppendTurnParams(raw json.RawMessage) (appendTurnParams, error) {
	var p appendTurnParams
	if len(raw) == 0 {
		return p, ErrMalformedTurnPayload
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return appendTurnParams{}, ErrMalformedTurnPayload
	}
	if p.ThreadID == "" || p.Role == "" {
		return appendTurnParams{}, ErrMalformedTurnPayload
	}
	return p, nil
}

// handleAppendTurn is chat.append_turn. It appends turn.Role+Segments to
// ThreadID via a.store (T1's append-only Store, so mutation history
// cannot be reopened through this adapter -- see adapter_test.go's
// TestHandleAppendTurn_CannotMutateHistory), then performs CLIENT-LOCAL
// ECHO: the SSE mirror is emitted BEFORE this handler returns, and a
// non-embedded-mode failure of that mirror is returned to the caller as
// an error rather than a reported success (the storage write is not
// rolled back -- Art.2's real-counterpart append already committed it --
// but the caller learns the echo did not happen instead of being told a
// turn is now visible when it is not).
func (a *Adapter) handleAppendTurn(ctx context.Context, raw json.RawMessage) (any, error) {
	params, err := decodeAppendTurnParams(raw)
	if err != nil {
		return nil, err
	}
	role, err := DecodeRole(params.Role)
	if err != nil {
		return nil, ErrMalformedTurnPayload
	}

	existing, err := a.store.ListTurns(ctx, params.ThreadID)
	if err != nil {
		return nil, err
	}
	seq := int64(len(existing))
	now := a.clock.Now().Unix()
	turn := Turn{ID: NewTurnID(params.ThreadID, seq, role), ThreadID: params.ThreadID, Seq: seq, Role: role, CreatedAt: now}

	segments := make([]Segment, 0, len(params.Segments))
	for i, sw := range params.Segments {
		kind, err := DecodeSegmentKind(sw.Kind)
		if err != nil {
			return nil, ErrMalformedTurnPayload
		}
		segments = append(segments, Segment{ID: NewSegmentID(turn.ID, int64(i), kind), TurnID: turn.ID, Seq: int64(i),
			Kind: kind, Content: sw.Content, CreatedAt: now})
	}

	// P1-E20-W5-S44-T4: when a JournalStore is configured (SetJournal),
	// the commit itself goes through AppendTurnJournaled -- a TurnAppend
	// intent is journaled BEFORE this store write, satisfying the entity
	// journal's "no partial store commit without a journal record"
	// contract for every real chat.append_turn call, not just this
	// package's own journal_test.go. a.journal is nil by default (T2's
	// original, still-tested no-journal path), matching bus/subst's own
	// optional-seam pattern above.
	if a.journal != nil {
		if err := AppendTurnJournaled(ctx, a.journal, a.store, turn, segments); err != nil {
			return nil, err
		}
	} else {
		if err := a.store.AppendTurn(ctx, turn); err != nil {
			return nil, err
		}
		for _, seg := range segments {
			if err := a.store.AppendSegment(ctx, seg); err != nil {
				return nil, err
			}
		}
	}

	if _, err := emitTurnAppended(ctx, a.bus, a.subst, a.mode, turn, segments); err != nil {
		if cascade.HasKind(err, cascade.KindUnsupported) {
			// Windows tier-2 embedded mode: the ticket's documented,
			// tested non-failure -- the turn is already committed above.
			return appendTurnResult{ThreadID: turn.ThreadID, TurnID: turn.ID, Seq: turn.Seq}, nil
		}
		return nil, err
	}
	return appendTurnResult{ThreadID: turn.ThreadID, TurnID: turn.ID, Seq: turn.Seq}, nil
}

// getThreadParams is chat.get_thread's wire request shape.
type getThreadParams struct {
	ThreadID string `json:"thread_id"`
}

// getThreadResult is chat.get_thread's wire response shape: the thread
// plus its full turn/segment history, read-only (this adapter offers no
// mutation surface besides AppendTurn/AppendSegment).
type getThreadResult struct {
	Thread Thread         `json:"thread"`
	Turns  []turnWithSegs `json:"turns"`
}

type turnWithSegs struct {
	Turn     Turn      `json:"turn"`
	Segments []Segment `json:"segments"`
}

func (a *Adapter) handleGetThread(ctx context.Context, raw json.RawMessage) (any, error) {
	var p getThreadParams
	if len(raw) == 0 {
		return nil, ErrMalformedTurnPayload
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil || p.ThreadID == "" {
		return nil, ErrMalformedTurnPayload
	}

	thread, ok, err := a.store.GetThread(ctx, p.ThreadID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, cascade.Wrapf(cascade.KindNotFound, ErrTurnNotFound, "conversation: thread not found")
	}
	turns, err := a.store.ListTurns(ctx, p.ThreadID)
	if err != nil {
		return nil, err
	}
	out := make([]turnWithSegs, 0, len(turns))
	for _, turn := range turns {
		segs, err := a.store.ListSegments(ctx, turn.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, turnWithSegs{Turn: turn, Segments: segs})
	}
	return getThreadResult{Thread: thread, Turns: out}, nil
}

// listThreadsResult is chat.list_threads's wire response shape.
type listThreadsResult struct {
	Threads []Thread `json:"threads"`
}

// handleListThreads is chat.list_threads. It takes no params; an object
// with any field is rejected, matching this adapter's other two
// handlers' DisallowUnknownFields discipline.
func (a *Adapter) handleListThreads(ctx context.Context, raw json.RawMessage) (any, error) {
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		var empty struct{}
		if err := dec.Decode(&empty); err != nil {
			return nil, ErrMalformedTurnPayload
		}
	}
	threads, err := a.store.ListThreads(ctx)
	if err != nil {
		return nil, err
	}
	return listThreadsResult{Threads: threads}, nil
}
