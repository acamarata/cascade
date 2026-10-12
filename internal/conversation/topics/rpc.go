// Purpose: chat.topics_list and chat.threads_list, the JSON-RPC methods
//   behind `cascade chat --topics`/`--threads`, over the real topic-engine
//   seams: NewConversationThreadStore reading the conversation tables
//   through conversation.Store, no second storage path.
// Inputs: a conversation.Store and a Clock at construction; {scope} for
//   topics_list, {page, page_size, scope} for threads_list.
// Outputs: topic types with thread/turn counts; a bounded page of topic
//   threads with topic_type, id, turn_count and last_activity.
// Constraints:
//   PACKAGE PLACEMENT: this package imports internal/conversation, so the
//   methods cannot sit in adapter.go (an import cycle); they are registered
//   from the composition root (cmd/cascade/chat_wiring.go) beside
//   Adapter.RegisterHandlers.
//   PRIVACY: every topic thread whose tier is SensitivityLocalOnly is
//   excluded from both methods for every caller, and a ThreadPrivacy read
//   failure fails the call (fail closed on uncertainty).
//   SCOPE: rows are filtered by conversation.CallerThreadView, so a
//   principal sees only threads attributed to its own scope; an optional
//   scope param narrows a view and never widens one.
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
	clock   Clock
}

// NewRPCHandlers wraps store as this file's real ThreadStore (through
// NewConversationThreadStore) and returns ready RPCHandlers. A nil store
// or clock is refused, matching every other constructor in this package.
func NewRPCHandlers(store conversation.Store, clock Clock) (*RPCHandlers, error) {
	threads, err := NewConversationThreadStore(store, clock)
	if err != nil {
		return nil, err
	}
	return &RPCHandlers{store: store, threads: threads, clock: clock}, nil
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

// handleTopicsList is chat.topics_list. Its only param is an optional
// scope; any other field is refused (DisallowUnknownFields).
func (h *RPCHandlers) handleTopicsList(ctx context.Context, raw json.RawMessage) (any, error) {
	scope, err := decodeTopicsListParams(raw)
	if err != nil {
		return nil, err
	}
	rows, err := h.listTopicThreads(ctx, scope)
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
	Page     int     `json:"page"`
	PageSize int     `json:"page_size"`
	Scope    *string `json:"scope,omitempty"`
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
	scope, err := declaredScope(p.Scope)
	if err != nil {
		return nil, err
	}
	rows, err := h.listTopicThreads(ctx, scope)
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

// decodeTopicsListParams reads topics_list's optional scope and refuses any
// other field.
func decodeTopicsListParams(raw json.RawMessage) (string, error) {
	var p struct {
		Scope *string `json:"scope"`
	}
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return "", cascade.Wrap(cascade.KindInvalidInput, err, "topics: chat.topics_list: malformed params")
		}
	}
	return declaredScope(p.Scope)
}

// declaredScope reads an optional scope param: absent is "", present must
// be non-empty. It only narrows the caller's view (CallerThreadView).
func declaredScope(scope *string) (string, error) {
	if scope == nil {
		return "", nil
	}
	if *scope == "" {
		return "", cascade.New(cascade.KindInvalidInput, "topics: scope must be a non-empty string")
	}
	return *scope, nil
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
func (h *RPCHandlers) listTopicThreads(ctx context.Context, scope string) ([]topicThreadRow, error) {
	visible, err := conversation.CallerThreadView(ctx, h.store, h.clock, scope)
	if err != nil {
		return nil, err
	}
	all, err := h.store.ListThreads(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]topicThreadRow, 0, len(all))
	for _, t := range all {
		topicType, ok := strings.CutPrefix(t.ID, topicThreadIDPrefix)
		if !ok || !visible(t.ID) {
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
