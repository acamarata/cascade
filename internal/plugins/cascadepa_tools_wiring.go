package plugins

import (
	"context"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	"github.com/acamarata/cascade/plugins/cascade-pa/tools"
)

// Purpose (this file): the production caller cascade-pa's three MCP tools
//   had none of. plugins/cascade-pa declares cascade_cpa_send,
//   cascade_cpa_history and cascade_cpa_search in its manifest, so every
//   connected harness SEES them — and until this file existed nothing
//   anywhere called SetConversations, so every call in a shipped binary
//   returned tools.ErrNoService. A seam with no production caller is not
//   a feature (R-14.283), and a registered tool that can only refuse is
//   the shape R-14.284 closed for `cascade chat`.
//
// Inputs: none at import time. Each call resolves the daemon socket path
//   lazily, exactly like cascadepa_wiring.go beside it, so importing this
//   package never touches the environment.
// Outputs: a tools.Conversations over the daemon's chat.append_turn,
//   chat.get_thread and chat.list_threads.
// Constraints: internal/plugins is the one package allowed to import both
//   internal/** and plugins/** (Art.10.2); cascade-pa itself may not
//   (R-14.69). Reuses rpcDoer, pathResolver and cascadePAClientTimeout
//   from cascadepa_wiring.go — same package, same transport, one timeout.
//
// SPORT: internal/plugins:cascadepa-tools-wiring (ADD) — P1-E20-W5-S43-T4.

func init() {
	cascadepa.SetConversations(newCascadePAConversations(
		client.UnixDialer, cascadePAClientTimeout, runtime.NewDefaultPathProvider))
}

// cascadePAConversations implements tools.Conversations over the daemon's
// JSON-RPC transport.
type cascadePAConversations struct {
	dial         client.DialFunc
	timeout      time.Duration
	resolvePaths pathResolver
	// doer, when non-nil, replaces the real client construction. Always
	// nil in production — see rpcDoer's doc comment in cascadepa_wiring.go
	// for why a same-package test is the only way to prove the success
	// mapping of an adapter this package may not reach over a real socket.
	doer rpcDoer
}

// newCascadePAConversations builds the adapter from its collaborators.
func newCascadePAConversations(dial client.DialFunc, timeout time.Duration, resolve pathResolver) *cascadePAConversations {
	return &cascadePAConversations{dial: dial, timeout: timeout, resolvePaths: resolve}
}

// rpc resolves the socket path and builds a client, per call.
func (c *cascadePAConversations) rpc() (rpcDoer, error) {
	if c.doer != nil {
		return c.doer, nil
	}
	paths, err := c.resolvePaths()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "cascade-pa tools: resolve daemon socket path")
	}
	return client.New(paths.SocketPath(), c.dial, c.timeout), nil
}

// Append records one turn through chat.append_turn.
//
// THEN READS THE THREAD BACK, for one reason: chat.append_turn's reply is
// {thread_id, turn_id, seq} and carries NO timestamp, while
// cascade_cpa_send's schema requires created_at as RFC3339. The timestamp
// is therefore read from the stored turn rather than invented here. That
// is a second round trip on a local unix socket, and it is the honest
// price of a reply shape that omits the field; the durable fix is for
// chat.append_turn to return created_at, which belongs to the conversation
// domain (S-43.T1/T2), not to this adapter.
func (c *cascadePAConversations) Append(ctx context.Context, req tools.AppendRequest) (tools.AppendResult, error) {
	rpc, err := c.rpc()
	if err != nil {
		return tools.AppendResult{}, err
	}
	var res appendTurnResult
	params := appendTurnParams{
		ThreadID: req.ThreadID,
		Role:     req.Role,
		Segments: []appendSegmentWire{{Kind: string(conversation.SegmentText), Content: req.Content}},
	}
	if err := rpc.Do(ctx, conversation.MethodAppendTurn, params, &res); err != nil {
		return tools.AppendResult{}, err
	}
	out := tools.AppendResult{ThreadID: res.ThreadID, TurnID: res.TurnID}
	detail, derr := c.thread(ctx, rpc, res.ThreadID)
	if derr != nil {
		// The turn IS recorded; only its timestamp is unavailable. Saying
		// so beats failing a write that succeeded.
		return out, nil
	}
	for _, t := range detail.Turns {
		if t.TurnID == res.TurnID {
			out.CreatedAt = t.CreatedAt
			break
		}
	}
	return out, nil
}

// Thread reads one thread's turns, oldest first.
func (c *cascadePAConversations) Thread(ctx context.Context, threadID string) (tools.ThreadDetail, error) {
	rpc, err := c.rpc()
	if err != nil {
		return tools.ThreadDetail{}, err
	}
	return c.thread(ctx, rpc, threadID)
}

// thread is Thread's body, split so Append can reuse it on a client it
// already built rather than resolving the socket path twice.
func (c *cascadePAConversations) thread(ctx context.Context, rpc rpcDoer, threadID string) (tools.ThreadDetail, error) {
	var res getThreadResultWire
	if err := rpc.Do(ctx, conversation.MethodGetThread, getThreadParamsWire{ThreadID: threadID}, &res); err != nil {
		return tools.ThreadDetail{}, err
	}
	out := tools.ThreadDetail{ThreadID: res.Thread.ID, Turns: make([]tools.TurnRecord, 0, len(res.Turns))}
	for _, tw := range res.Turns {
		out.Turns = append(out.Turns, tools.TurnRecord{
			TurnID:    tw.Turn.ID,
			Role:      string(tw.Turn.Role),
			Content:   joinSegmentText(tw.Segments),
			CreatedAt: rfc3339(tw.Turn.CreatedAt),
			Seq:       tw.Turn.Seq,
		})
	}
	return out, nil
}

// Threads lists every thread.
//
// UPDATED_AT IS DERIVED, and costs one extra read per thread.
// chat.list_threads returns conversation.Thread, which carries ID, Name
// and CreatedAt and no modification time at all — while
// cascade_cpa_history's listing schema requires updated_at. A thread's
// only mutation is an appended turn, so the newest turn's created_at IS
// its updated_at; that is derivation, not a substitute. Reporting the
// thread's own CreatedAt under the name updated_at would be wrong data
// under a right-looking name, and omitting the field would break the
// declared schema. The durable fix is for chat.list_threads to carry it —
// again the conversation domain's, not this adapter's.
func (c *cascadePAConversations) Threads(ctx context.Context) ([]tools.ThreadSummary, error) {
	rpc, err := c.rpc()
	if err != nil {
		return nil, err
	}
	var res listThreadsResultWire
	if err := rpc.Do(ctx, conversation.MethodListThreads, struct{}{}, &res); err != nil {
		return nil, err
	}
	out := make([]tools.ThreadSummary, 0, len(res.Threads))
	for _, th := range res.Threads {
		out = append(out, tools.ThreadSummary{
			ID:        th.ID,
			Title:     th.Name,
			UpdatedAt: c.threadUpdatedAt(ctx, rpc, th),
		})
	}
	return out, nil
}

// threadUpdatedAt returns the newest turn's timestamp, falling back to the
// thread's own creation time when it has no turns or cannot be read.
func (c *cascadePAConversations) threadUpdatedAt(ctx context.Context, rpc rpcDoer, th conversation.Thread) string {
	detail, err := c.thread(ctx, rpc, th.ID)
	if err != nil || len(detail.Turns) == 0 {
		return rfc3339(th.CreatedAt)
	}
	return detail.Turns[len(detail.Turns)-1].CreatedAt
}

// Search runs the daemon's FTS5 index through chat.search.
//
// A store with no index answers with conversation.ErrSearchUnavailable,
// which crosses the wire as TEXT: the taxonomy kind survives, the identity
// does not. Recognising it takes BOTH halves, and neither alone will do.
//
// The kind alone is too broad — KindUnsupported is shared with everything
// else the daemon refuses. The message alone is forgeable: review found
// that a QUERY containing the marker text comes back inside SQLite's own
// MATCH syntax error, which a substring test would mistake for a missing
// index and answer with a silent scan. That error is KindInvalidInput
// (translateSearchError), so requiring the kind as well closes it.
func (c *cascadePAConversations) Search(ctx context.Context, req tools.SearchRequest) ([]tools.SearchResult, error) {
	rpc, err := c.rpc()
	if err != nil {
		return nil, err
	}
	var res searchResultSetWire
	params := searchParamsWire{Query: req.Query, ThreadID: req.ThreadID, Limit: req.Limit}
	if err := rpc.Do(ctx, conversation.MethodSearch, params, &res); err != nil {
		if cascade.HasKind(err, cascade.KindUnsupported) &&
			strings.Contains(err.Error(), searchUnavailableMarker) {
			return nil, tools.ErrSearchUnavailable
		}
		return nil, err
	}
	out := make([]tools.SearchResult, 0, len(res.Results))
	for _, r := range res.Results {
		out = append(out, tools.SearchResult{
			ThreadID: r.ThreadID, TurnID: r.TurnID, Content: r.Content, Score: r.Score,
		})
	}
	return out, nil
}

// searchUnavailableMarker is the part of conversation.ErrSearchUnavailable's
// message that names the actual condition. It is a literal rather than a
// slice of the sentinel: a marker derived by trimming a hardcoded prefix
// would silently become the WHOLE message if that prefix ever changed, and
// still match — which is not the safety the derivation appeared to give.
// The literal is pinned to the sentinel by a test instead, which fails
// loudly if the two drift.
const searchUnavailableMarker = "no fts5 index on this store"

// joinSegmentText flattens a turn's segments into the one content string
// the tool schemas expose.
//
// Every segment kind contributes its text, separated by a blank line. A
// code or tool_result segment dropped here would make a turn searchable
// only by the prose around it, and `cascade_cpa_search` would then report
// no match on content the operator can plainly see in the transcript.
func joinSegmentText(segs []conversation.Segment) string {
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		if s.Content != "" {
			parts = append(parts, s.Content)
		}
	}
	return strings.Join(parts, "\n\n")
}

// rfc3339 renders a stored unix-second timestamp in the format the tool
// schemas declare. A zero timestamp renders empty rather than as 1970,
// which would be a fact nobody recorded.
func rfc3339(unixSeconds int64) string {
	if unixSeconds == 0 {
		return ""
	}
	return time.Unix(unixSeconds, 0).UTC().Format(time.RFC3339)
}

// The client-side decode targets for chat.get_thread and chat.list_threads.
//
// These embed internal/conversation's OWN exported domain types rather
// than re-declaring their fields. conversation.Thread/Turn/Segment carry
// no json tags, so they encode under their Go field names — duplicating
// that by hand is a drift waiting to happen, and this package is allowed
// to import them. (cascadepa_wiring.go duplicates append_turn's shapes
// only because those particular types are unexported.)
type getThreadParamsWire struct {
	ThreadID string `json:"thread_id"`
}

type getThreadResultWire struct {
	Thread conversation.Thread `json:"thread"`
	Turns  []turnWithSegsWire  `json:"turns"`
}

type turnWithSegsWire struct {
	Turn     conversation.Turn      `json:"turn"`
	Segments []conversation.Segment `json:"segments"`
}

type listThreadsResultWire struct {
	Threads []conversation.Thread `json:"threads"`
}

// The client-side shapes for chat.search.
type searchParamsWire struct {
	Query    string `json:"query"`
	ThreadID string `json:"thread_id"`
	Limit    int    `json:"limit"`
}

type searchResultWire struct {
	ThreadID string  `json:"thread_id"`
	TurnID   string  `json:"turn_id"`
	Content  string  `json:"content"`
	Score    float64 `json:"score"`
}

type searchResultSetWire struct {
	Results []searchResultWire `json:"results"`
}
