package conversation

// Purpose: the chat.* JSON-RPC surface over Store: chat.append_turn,
//   chat.get_thread, chat.list_threads, chat.search and chat.create_thread
//   (adapter_createthread.go), one rpc.Registry.Register call per method.
// Inputs: a Store, an EventBus (nil in embedded mode), a Substitutor (nil is
//   sse.go's passthrough), a Clock, and a mode ("" daemon, ModeEmbedded).
// Outputs: wire result shapes, or a pkg/cascade error mapped by internal/rpc
//   (adapter_errors.go explains why there is no second mapping layer).
// Constraints: CLIENT-LOCAL ECHO: handleAppendTurn emits the SSE mirror after
//   the store commit and before returning, and reports a non-embedded mirror
//   failure instead of a success no client will see. SCOPE: every verb is
//   owner-view (internal/rpc/access_table_conversation.go); reads run through
//   scope_attr.go's wrappers and appends through scopedAppendThread, so a
//   principal reaches only its own scope's threads.
// SPORT: internal.conversation.adapter/ADDED (P1-E20-W5-S43-T2).

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The chat.* JSON-RPC 2.0 method names this adapter registers.
const (
	MethodAppendTurn  = "chat.append_turn"
	MethodGetThread   = "chat.get_thread"
	MethodListThreads = "chat.list_threads"
	// MethodSearch is S-44.T3's FTS5 search, reachable over the wire.
	// Without it SearchTurns is a Go method no running program can call.
	MethodSearch = "chat.search"
)

// Adapter binds T1's Store to the D/S-06.T3 method registry and the SSE
// mirror. The zero value is not usable; construct with NewAdapter.
type Adapter struct {
	store   Store
	bus     EventBus
	subst   Substitutor
	clock   Clock
	mode    string
	journal JournalStore  // optional; nil means "journal.go's write path is not wired here" -- see SetJournal
	scrub   ScrubPipeline // optional; nil is scrub.go's documented passthrough -- see SetScrub
	topics  TopicObserver // optional; nil means "no topic engine composed" -- see SetTopicObserver (adapter_topics.go)
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

// SetScrub wires p (scrub.go's S-44.T1 pipeline) into handleAppendTurn.
// Nil is scrub.go's documented passthrough, matching SetJournal above.
func (a *Adapter) SetScrub(p ScrubPipeline) { a.scrub = p }

// RegisterHandlers binds this adapter's three methods onto registry. See
// this file's CONTRACT DEVIATION note for the composition-root call site.
func (a *Adapter) RegisterHandlers(registry *rpc.Registry) {
	registry.Register(MethodAppendTurn, mapped(a.handleAppendTurn))
	registry.Register(MethodGetThread, mapped(a.scopedGetThread))
	registry.Register(MethodListThreads, mapped(a.scopedListThreads))
	registry.Register(MethodSearch, mapped(a.scopedSearch))
	registry.Register(MethodCreateThread, mapped(a.handleCreateThread)) // adapter_createthread.go
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
	params.ThreadID, err = a.scopedAppendThread(ctx, params)
	if err != nil {
		return nil, err
	}

	existing, err := a.store.ListTurns(ctx, params.ThreadID)
	if err != nil {
		return nil, err
	}
	seq := int64(len(existing))
	now := a.clock.Now().Unix()
	turn := Turn{ID: NewTurnID(params.ThreadID, seq, role), ThreadID: params.ThreadID, Seq: seq, Role: role, CreatedAt: now}

	segments, err := decodeSegments(params.Segments, turn.ID, now)
	if err != nil {
		return nil, err
	}
	if segments, err = a.scrubSegments(ctx, segments); err != nil { // S-44.T1: before journal/store/SSE
		return nil, err
	}

	// With a JournalStore (SetJournal) the TurnAppend intent is journaled
	// BEFORE the store write: no partial commit without a journal record.
	// a.journal is nil by default, the original no-journal path.
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
			return a.finishAppend(ctx, turn, segments), nil
		}
		return nil, err
	}
	return a.finishAppend(ctx, turn, segments), nil
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

// scopedSearch is chat.search over the caller's view. A narrowed view runs
// the query per visible thread, so a foreign hit never takes a slot.
func (a *Adapter) scopedSearch(ctx context.Context, raw json.RawMessage) (any, error) {
	rest, declared, err := splitScopeParam(raw)
	if err != nil {
		return nil, err
	}
	v, err := resolveView(ctx, a.store, a.clock, declared)
	if err != nil {
		return nil, err
	} else if v.all {
		return a.handleSearch(ctx, rest)
	}
	p, err := decodeSearchParams(rest)
	if err != nil {
		return nil, err
	}
	if p.ThreadID != "" && !v.admits(p.ThreadID) {
		return nil, errScopeDenied
	}
	ids := []string{p.ThreadID}
	if p.ThreadID == "" {
		ids = ids[:0]
		for id := range v.ids {
			ids = append(ids, id)
		}
		sort.Strings(ids)
	}
	var hits []TurnMatch
	for _, id := range ids {
		m, err := a.store.SearchTurns(ctx, p.Query, SearchFilter{ThreadID: id, Limit: p.Limit})
		if err != nil {
			return nil, err
		}
		hits = append(hits, m...)
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Rank < hits[j].Rank })
	if limit := clampLimit(p.Limit); len(hits) > limit {
		hits = hits[:limit]
	}
	return a.rankedResults(ctx, hits)
}

// rankedResults renders narrowed hits. The score is the hit's rank position,
// not bm25: bm25 weighs terms by the whole index, foreign threads included.
func (a *Adapter) rankedResults(ctx context.Context, hits []TurnMatch) (any, error) {
	out := make([]searchResultWire, 0, len(hits))
	for i, m := range hits {
		segs, err := a.store.ListSegments(ctx, m.Turn.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, searchResultWire{ThreadID: m.Turn.ThreadID, TurnID: m.Turn.ID,
			Content: joinSegmentContent(segs), Score: float64(len(hits) - i)})
	}
	return searchResultSet{Results: out}, nil
}
