package sessions

// Purpose (this file): the fleet.sessions.list JSON-RPC 2.0 door
//
//	(R-21.272/R-21.273): a server-side HandlerFunc RegisterHandlers binds
//	into an *rpc.Registry, plus a typed Client wrapper mirroring
//	pkg/provider/client.go's ModelExecute pattern for L/S-25.T2's
//	one-shot mode.
//
// Inputs: fleet.sessions.list's params (an optional Filter, JSON-decoded
//
//	with unknown fields rejected).
//
// Outputs: the filtered []SessionRecord, or a pkg/cascade taxonomy error.
// Constraints: Windows has no daemon at all (tier-2) — Client.List refuses
//
//	before ever calling caller.Do when the daemonless state on ctx reports
//	Embedded on windows, exactly as internal/rpc/sse.go's ServeHTTP
//	refuses unconditionally on windows (see this package's sse.go).
//
// CONTRACT DEVIATION (wiring, recorded, not papered over). R-21.273 lists
// internal/rpc/registry.go in files_scope.change "for every ticket that
// registers RPC methods," but Registry (registry.go) is a fully generic
// name->HandlerFunc map with no method-specific code — every landed
// precedent that registers a method (context.scope.show, memory.*,
// recall.*) instead adds a Register*Handler function in internal/daemon
// and calls it from cmd/cascade/daemon_unix_run.go's buildRPCServer,
// neither of which is in this ticket's files_scope. RegisterHandlers
// below is that same shape, ready for buildRPCServer to call; the actual
// call site is out of scope here, recorded in
// internal/build/testonly-allow.json naming this ticket, exactly as this
// ticket's own instructions direct when wiring is genuinely out of scope.
//
// SPORT: internal.fleet.sessions.RegisterHandlers/ADDED,
//
//	internal.fleet.sessions.Client/ADDED (P1-E12-W3-S24-T3).

import (
	"bytes"
	"context"
	"encoding/json"
	goruntime "runtime"
	"sync"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// MethodList is the fleet.sessions.list JSON-RPC 2.0 method name
// (R-21.272).
const MethodList = "fleet.sessions.list"

// listParams is fleet.sessions.list's wire request shape. Unknown fields
// are rejected (see decodeListParams) rather than silently ignored, so a
// caller that misspells a filter field learns immediately.
type listParams struct {
	Harness         *string `json:"harness,omitempty"`
	Account         *string `json:"account,omitempty"`
	State           *string `json:"state,omitempty"`
	ParentSessionID *string `json:"parent_session_id,omitempty"`
}

// listResult is fleet.sessions.list's wire response shape.
type listResult struct {
	Sessions []SessionRecord `json:"sessions"`
}

// ErrUnknownFilterField is returned when the RPC request's params object
// carries a key outside listParams's known set.
var ErrUnknownFilterField = cascade.New(cascade.KindInvalidInput, "sessions: unknown filter field")

// decodeListParams decodes raw into a Filter, rejecting any key raw
// carries that listParams does not declare (json.Decoder's
// DisallowUnknownFields), which is how "an unknown filter field" is
// detected: this package defines no separate allow-list, the wire shape
// IS the allow-list.
func decodeListParams(raw json.RawMessage) (Filter, error) {
	if len(raw) == 0 {
		return Filter{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p listParams
	if err := dec.Decode(&p); err != nil {
		return Filter{}, cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownFilterField, "sessions: decoding fleet.sessions.list params: %v", err)
	}
	return Filter(p), nil
}

// RegisterHandlers binds fleet.sessions.list to registry. See this file's
// CONTRACT DEVIATION note for why the composition-root call site
// (cmd/cascade/daemon_unix_run.go's buildRPCServer) is out of this
// ticket's files_scope.
func RegisterHandlers(registry *rpc.Registry, store *Store) {
	registry.Register(MethodList, func(ctx context.Context, raw json.RawMessage) (any, error) {
		filter, err := decodeListParams(raw)
		if err != nil {
			return nil, err
		}
		records, err := store.List(ctx, filter)
		if err != nil {
			return nil, err
		}
		if c, ok := principal.CallerFrom(ctx); ok && c.Principal != nil {
			if records, err = store.subtreeOnly(ctx, c.Principal.SessionID, records); err != nil {
				return nil, err
			}
		}
		return listResult{Sessions: records}, nil
	})
}

// RPCCaller is the minimal one-method interface the frozen D/S-07.T3
// client (internal/client.Client) already satisfies (R-21.281), declared
// locally so this package never imports internal/client directly —
// mirroring pkg/provider/client.go's identical RPCCaller seam.
type RPCCaller interface {
	Do(ctx context.Context, method string, params, out any) error
}

// Client wraps an RPCCaller with the typed fleet.sessions.list door.
type Client struct {
	caller RPCCaller
}

// NewClient builds a Client dialing every call through c.
func NewClient(c RPCCaller) *Client {
	return &Client{caller: c}
}

// ErrWindowsTier2Unavailable is the structured refusal fleet.sessions.list
// and the SSE stream both return on Windows: no daemon exists there at
// all (tier-2), so there is nothing to dial.
var ErrWindowsTier2Unavailable = cascade.New(cascade.KindUnsupported, "sessions: fleet.sessions unavailable on Windows tier-2 (no daemon)")

// List calls fleet.sessions.list through c.caller, unless this process is
// running on Windows — which has no daemon at all (tier-2) — in which
// case it refuses with ErrWindowsTier2Unavailable before ever calling
// caller.Do, exactly the unconditional GOOS check sse.go's ServeHTTP
// makes for the same reason.
func (c *Client) List(ctx context.Context, filter Filter) ([]SessionRecord, error) {
	if goruntime.GOOS == "windows" {
		return nil, ErrWindowsTier2Unavailable
	}
	params := listParams(filter)
	var resp listResult
	if err := c.caller.Do(ctx, MethodList, params, &resp); err != nil {
		if _, ok := cascade.KindOf(err); ok {
			return nil, err
		}
		return nil, cascade.Wrap(cascade.KindInternal, err, "sessions: fleet.sessions.list failed")
	}
	return resp.Sessions, nil
}

// errSessionScope is the one refusal a principal receives for a session
// outside its subtree, whether that session exists or not.
var errSessionScope = cascade.New(cascade.KindPermissionDenied, "sessions: session outside caller scope")

// subtreeOnly keeps the records in root's subtree, resolving ancestry from
// one scan of every stored session.
func (s *Store) subtreeOnly(ctx context.Context, root string, records []SessionRecord) ([]SessionRecord, error) {
	all, err := s.List(ctx, Filter{})
	if err != nil {
		return nil, err
	}
	parents := make(map[string]string, len(all))
	for _, rec := range all {
		if rec.ParentSessionID != nil {
			parents[rec.SessionID] = *rec.ParentSessionID
		}
	}
	lookup := func(id string) (string, bool) { p, ok := parents[id]; return p, ok }
	out := []SessionRecord{}
	for _, rec := range records {
		if inSubtree(root, rec.SessionID, lookup) {
			out = append(out, rec)
		}
	}
	return out, nil
}

// SessionBinding names the session-addressed verbs a principal is bound
// to: Methods carry a session_id that must lie in the caller's subtree;
// Creator is the one method that may create a new session, for the event
// types in StartEvents.
type SessionBinding struct {
	Methods     []string
	Creator     string
	StartEvents []string
}

// sessionRef is the part of a bound method's params this binding reads.
type sessionRef struct {
	SessionID string `json:"session_id"`
	EventType string `json:"event_type"`
}

// ScopeMiddleware confines a principal to its session subtree. On every
// principal dispatch it installs the Visibility predicate (rpc.WithEventVisible)
// for handlers that filter by session; on a bound method it refuses a
// session_id outside the subtree before the handler runs. A principal can
// create only its own daemon-minted session, linked to its minted parent; it
// never creates another id, so it cannot claim an id another entity uses. A
// start event never drops an existing parent link, for any caller. Bound
// calls are serialized, so admission, creation and linking are one step and
// no concurrent update writes back a record without its new parent link.
func ScopeMiddleware(store *Store, b SessionBinding) rpc.MiddlewareFunc {
	bound := make(map[string]bool, len(b.Methods))
	for _, m := range b.Methods {
		bound[m] = true
	}
	starts := make(map[string]bool, len(b.StartEvents))
	for _, e := range b.StartEvents {
		starts[e] = true
	}
	var startMu sync.Mutex // serializes every bound call
	return func(method string, next rpc.HandlerFunc) rpc.HandlerFunc {
		return func(ctx context.Context, raw json.RawMessage) (any, error) {
			c, ok := principal.CallerFrom(ctx)
			isPrincipal := ok && c.Principal != nil
			if isPrincipal {
				ctx = rpc.WithEventVisible(ctx, store.Visibility(*c.Principal))
			}
			if !bound[method] {
				return next(ctx, raw)
			}
			var ref sessionRef
			refErr := json.Unmarshal(raw, &ref)
			start := refErr == nil && method == b.Creator && starts[ref.EventType]
			startMu.Lock()
			defer startMu.Unlock()
			if !isPrincipal {
				if start {
					return store.startSession(ctx, ref.SessionID, "", raw, next)
				}
				return next(ctx, raw)
			}
			if refErr != nil {
				return nil, errSessionScope
			}
			return store.bindSession(ctx, *c.Principal, ref, start, raw, next)
		}
	}
}

// bindSession admits a principal's bound call only for its own session or a
// descendant. Its own session, once started, is linked to its minted parent.
func (s *Store) bindSession(ctx context.Context, p principal.Principal, ref sessionRef, start bool, raw json.RawMessage, next rpc.HandlerFunc) (any, error) {
	if p.SessionID == "" || !inSubtree(p.SessionID, ref.SessionID, s.parentLookup(ctx)) {
		return nil, errSessionScope
	}
	if !start {
		return next(ctx, raw)
	}
	parent := ""
	if ref.SessionID == p.SessionID {
		parent = p.ParentSessionID
	}
	return s.startSession(ctx, ref.SessionID, parent, raw, next)
}

// startSession runs a start event and then restores the session's parent
// link: the prior parent when the session already existed, else parent.
// The start event's own Upsert carries no parent field. A link that would
// close a cycle (parent already under id) is never written.
func (s *Store) startSession(ctx context.Context, id, parent string, raw json.RawMessage, next rpc.HandlerFunc) (any, error) {
	if prior, err := s.getLocked(ctx, id); err == nil {
		parent = ""
		if prior.ParentSessionID != nil {
			parent = *prior.ParentSessionID
		}
	}
	res, err := next(ctx, raw)
	if err != nil || parent == "" || inSubtree(id, parent, s.parentLookup(ctx)) {
		return res, err
	}
	rec, err := s.getLocked(ctx, id)
	if err != nil {
		return nil, err
	}
	if rec.ParentSessionID != nil && *rec.ParentSessionID == parent {
		return res, nil
	}
	rec.ParentSessionID = &parent
	if err := s.Upsert(ctx, rec); err != nil {
		return nil, err
	}
	return res, nil
}
