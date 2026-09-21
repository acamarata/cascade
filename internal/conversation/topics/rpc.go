// Purpose: chat.topics_list and chat.threads_list, the two JSON-RPC
//   methods P1-E21-W5-S46-T4 adds so `cascade chat --topics`/`--threads`
//   has a real daemon-side counterpart, over the REAL topic-engine seams:
//   NewConversationThreadStore reading the conversation domain's own
//   tables through conversation.Store, no second storage path.
// Inputs: a conversation.Store and a Clock at construction; no params for
//   topics_list, {page, page_size} for threads_list.
// Outputs: topic types with thread/turn counts; a bounded page of topic
//   threads with topic_type, id, turn_count and last_activity.
// Constraints:
//
//   PACKAGE PLACEMENT (recorded contract deviation, not papered over).
//   The ticket's contract places these methods "next to chat.list_threads
//   in adapter.go" (package conversation). That is not buildable:
//   thread_store.go (this package) imports internal/conversation for
//   NewConversationThreadStore's own conversation.Store parameter, so
//   internal/conversation importing internal/conversation/topics back
//   would be a compile-time import cycle (`go build ./...` refuses it,
//   the same class of proof internal/plugins/plugin_rpc.go's own
//   CONTRACT NOTE already used for an identical placement conflict on
//   this ticket's neighbor package). This file therefore lives in
//   package topics, which already depends one-way on conversation, and is
//   registered from the composition root (cmd/cascade/chat_wiring.go)
//   alongside Adapter.RegisterHandlers -- exactly where every other
//   chat.* registration call already happens per adapter.go's own
//   CONTRACT DEVIATION note (the daemon composition root, not
//   internal/rpc/registry.go itself, is the one real call site).
//
//   PRIVACY (recorded deviation from the ticket's literal text). The
//   ticket says these two methods must "honour thread privacy modes
//   exactly as chat.list_threads does ... reuse the same privacy filter,
//   never a copy". Verified against the live tree: handleListThreads
//   (adapter.go) calls a.store.ListThreads(ctx) unconditionally and
//   applies NO privacy filter at all -- there is no existing filter to
//   reuse. Rather than silently matching that (which would expose a
//   local-only topic thread's existence through a new listing surface)
//   or fabricating a "reuse" that does not exist, this file adds a real,
//   new filter using the real primitive already in the tree
//   (conversation.Store.ThreadPrivacy, P1-E20-W5-S44-T2): every
//   topic-prefixed thread whose tier is provider.SensitivityLocalOnly is
//   excluded from both methods, unconditionally (there is no
//   caller-locality signal at the RPC layer to condition on, so the
//   fail-closed choice is to exclude it from every caller, which is
//   strictly a SUPERSET of "never listed through an external-capable
//   path"). A ThreadPrivacy read failure propagates as the call's own
//   error rather than being read as "not local-only" (fail closed on
//   uncertainty).
// SPORT: internal/conversation/topics rpc (ADD) (P1-E21-W5-S46-T4).

package topics

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// The chat.* JSON-RPC 2.0 method names this file registers, matching
// internal/conversation/adapter.go's MethodAppendTurn/MethodGetThread/
// MethodListThreads naming convention exactly.
const (
	MethodTopicsList  = "chat.topics_list"
	MethodThreadsList = "chat.threads_list"
)

// rpcPageDefault/rpcPageMax bound threads_list's page_size, matching
// internal/conversation/pagination.go's defaultPageSize/maxPageSize
// values (this package cannot import those unexported constants, so the
// values are restated, not derived -- both are small, stable, and
// documented in the sibling file this one is the closest analogue to).
const (
	rpcPageDefault = 20
	rpcPageMax     = 100
)

// RPCHandlers binds chat.topics_list and chat.threads_list to a real
// ThreadStore (over a conversation.Store, via NewConversationThreadStore)
// and the same conversation.Store, for the privacy read every listing
// applies (see this file's header). The zero value is not usable;
// construct with NewRPCHandlers.
type RPCHandlers struct {
	store   conversation.Store
	threads ThreadStore
}

// NewRPCHandlers wraps store as this file's real ThreadStore (through
// NewConversationThreadStore) and returns ready RPCHandlers. A nil store
// or clock is refused, matching every other constructor in this package.
func NewRPCHandlers(store conversation.Store, clock Clock) (*RPCHandlers, error) {
	threads, err := NewConversationThreadStore(store, clock)
	if err != nil {
		return nil, err
	}
	return &RPCHandlers{store: store, threads: threads}, nil
}

// RegisterHandlers binds both methods onto registry. See this file's
// header for the composition-root call site (cmd/cascade/chat_wiring.go).
func (h *RPCHandlers) RegisterHandlers(registry *rpc.Registry) {
	registry.Register(MethodTopicsList, h.handleTopicsList)
	registry.Register(MethodThreadsList, h.handleThreadsList)
}

// topicSummaryWire is one chat.topics_list row.
type topicSummaryWire struct {
	TopicType   string `json:"topic_type"`
	ThreadCount int    `json:"thread_count"`
	TurnCount   int    `json:"turn_count"`
}

// topicsListResult is chat.topics_list's wire response shape.
type topicsListResult struct {
	Topics []topicSummaryWire `json:"topics"`
}

// handleTopicsList is chat.topics_list. It takes no params, matching
// handleListThreads' own DisallowUnknownFields discipline for an
// object with any field.
func (h *RPCHandlers) handleTopicsList(ctx context.Context, raw json.RawMessage) (any, error) {
	if err := refuseNonEmptyParams(raw); err != nil {
		return nil, err
	}
	rows, err := h.listTopicThreads(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]topicSummaryWire, 0, len(rows))
	for _, r := range rows {
		out = append(out, topicSummaryWire{TopicType: r.topicType, ThreadCount: 1, TurnCount: r.turnCount})
	}
	return topicsListResult{Topics: out}, nil
}

// threadSummaryWire is one chat.threads_list row.
type threadSummaryWire struct {
	TopicType    string `json:"topic_type"`
	ID           string `json:"id"`
	TurnCount    int    `json:"turn_count"`
	LastActivity int64  `json:"last_activity"` // unix seconds, 0 if empty
}

// threadsListParams is chat.threads_list' wire request shape: a 1-based
// page and a page size, matching the CLI's own --page/--page-size flags
// directly (no cursor translation at the client-glue layer).
type threadsListParams struct {
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
}

// threadsListResult is chat.threads_list' wire response shape.
type threadsListResult struct {
	Threads    []threadSummaryWire `json:"threads"`
	Page       int                 `json:"page"`
	PageSize   int                 `json:"page_size"`
	TotalCount int                 `json:"total_count"`
}

// handleThreadsList is chat.threads_list.
func (h *RPCHandlers) handleThreadsList(ctx context.Context, raw json.RawMessage) (any, error) {
	p, err := decodeThreadsListParams(raw)
	if err != nil {
		return nil, err
	}
	rows, err := h.listTopicThreads(ctx)
	if err != nil {
		return nil, err
	}
	page, pageSize := clampPage(p.Page), clampPageSize(p.PageSize)
	start := (page - 1) * pageSize
	out := make([]threadSummaryWire, 0, pageSize)
	if start < len(rows) {
		end := start + pageSize
		if end > len(rows) {
			end = len(rows)
		}
		for _, r := range rows[start:end] {
			out = append(out, threadSummaryWire{
				TopicType: r.topicType, ID: r.id, TurnCount: r.turnCount, LastActivity: r.lastActivity,
			})
		}
	}
	return threadsListResult{Threads: out, Page: page, PageSize: pageSize, TotalCount: len(rows)}, nil
}

// decodeThreadsListParams decodes threads_list's params. Missing/empty
// params is not malformed -- it means "page 1, default page size",
// matching handleAppendTurn's own "absence names the default" posture.
func decodeThreadsListParams(raw json.RawMessage) (threadsListParams, error) {
	var p threadsListParams
	if len(raw) == 0 {
		return p, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return threadsListParams{}, cascade.Wrap(cascade.KindInvalidInput, err,
			"topics: chat.threads_list: malformed params")
	}
	return p, nil
}

// refuseNonEmptyParams rejects any object with a field, matching
// handleListThreads' own no-params discipline.
func refuseNonEmptyParams(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var empty struct{}
	if err := dec.Decode(&empty); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "topics: chat.topics_list: malformed params")
	}
	return nil
}

func clampPage(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

func clampPageSize(n int) int {
	if n <= 0 {
		return rpcPageDefault
	}
	if n > rpcPageMax {
		return rpcPageMax
	}
	return n
}

// topicThreadRow is one topic-prefixed, non-local-only thread this file's
// two handlers both read from -- computed once by listTopicThreads and
// projected into each method's own wire shape.
type topicThreadRow struct {
	topicType    string
	id           string
	turnCount    int
	lastActivity int64
}

// listTopicThreads scans every conversation thread, keeps only the
// topic-engine's own ("topic:"-prefixed, see thread_store.go's
// topicThreadIDPrefix) threads, excludes every SensitivityLocalOnly one
// (this file's header PRIVACY note), and sorts the result by TopicType so
// threads_list' pagination is stable across calls. A ThreadPrivacy or
// ListTurns failure on any one thread fails the whole call -- never a
// partial, silently-shorter list.
func (h *RPCHandlers) listTopicThreads(ctx context.Context) ([]topicThreadRow, error) {
	all, err := h.store.ListThreads(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]topicThreadRow, 0, len(all))
	for _, t := range all {
		topicType, ok := strings.CutPrefix(t.ID, topicThreadIDPrefix)
		if !ok {
			continue
		}
		tier, err := h.store.ThreadPrivacy(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		if tier == provider.SensitivityLocalOnly {
			continue
		}
		turns, err := h.store.ListTurns(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		var last int64
		if n := len(turns); n > 0 {
			last = turns[n-1].CreatedAt
		}
		out = append(out, topicThreadRow{topicType: topicType, id: t.ID, turnCount: len(turns), lastActivity: last})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].topicType < out[j].topicType })
	return out, nil
}
