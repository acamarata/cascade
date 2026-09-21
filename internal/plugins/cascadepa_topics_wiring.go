package plugins

import (
	"context"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/conversation/topics"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	pacmd "github.com/acamarata/cascade/plugins/cascade-pa/cmd"
)

// Purpose (this file): closes P1-E21-W5-S46-T4's own composition-root gap
//
//	for the topics/threads CLI surface, the same shape
//	cascadepa_wiring.go's init() already closes for pacmd.SetClient:
//	injects a real internal/client-backed implementation of
//	plugins/cascade-pa/cmd.TopicsThreadsClient so `cascade chat
//	--topics`/`--threads`/`--thread <slug>` reach the daemon's real
//	chat.topics_list / chat.threads_list / chat.get_thread doors instead
//	of the package default unconfiguredTopicsThreadsClient.
//
// Inputs: none at import time -- see cascadepa_wiring.go's identical
//
//	lazy-path-resolution rationale.
//
// Outputs: registers a *cascadePATopicsClient with plugins/cascade-pa/cmd
//
//	via SetTopicsThreadsClient. ListTopics/ListThreads dial the two new
//	RPC doors internal/conversation/topics/rpc.go registers; OpenThread
//	dials the EXISTING chat.get_thread door (internal/conversation's own
//	MethodGetThread) rather than inventing a third — `--thread <slug>`
//	names any existing thread, topic-engine or not, and get_thread
//	already answers that question for chat.go's own JSON-open path.
//
// Constraints: this package is the one place allowed to import both
//
//	internal/** and plugins/** (Art.10.2) -- see cascadepa_wiring.go's
//	identical constraint note.
//
// SPORT: internal/plugins:cascadepa-topics-wiring (ADD) -- P1-E21-W5-S46-T4.

func init() {
	pacmd.SetTopicsThreadsClient(newCascadePATopicsClient(client.UnixDialer, cascadePAClientTimeout, runtime.NewDefaultPathProvider))
}

// cascadePATopicsClient adapts internal/client's unix-socket JSON-RPC
// transport to plugins/cascade-pa/cmd.TopicsThreadsClient, mirroring
// cascadePAClient's shape and lazy path resolution exactly.
type cascadePATopicsClient struct {
	dial         client.DialFunc
	timeout      time.Duration
	resolvePaths pathResolver
	// doer, when non-nil, replaces the real rpcClient() construction --
	// see rpcDoer's own doc comment in cascadepa_wiring.go. Always nil in
	// production.
	doer rpcDoer
}

func newCascadePATopicsClient(dial client.DialFunc, timeout time.Duration, resolvePaths pathResolver) *cascadePATopicsClient {
	return &cascadePATopicsClient{dial: dial, timeout: timeout, resolvePaths: resolvePaths}
}

func (c *cascadePATopicsClient) rpcClient() (rpcDoer, error) {
	if c.doer != nil {
		return c.doer, nil
	}
	paths, err := c.resolvePaths()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "cascade chat --topics/--threads: resolve daemon socket path")
	}
	return client.New(paths.SocketPath(), c.dial, c.timeout), nil
}

// topicsListWire/topicSummaryWire mirror
// internal/conversation/topics/rpc.go's topicsListResult/topicSummaryWire
// exactly -- duplicated client-side rather than imported, matching
// cascadepa_wiring.go's own appendTurnParams precedent (that package's
// wire shapes are unexported, the server side's decode target, not a
// public SDK).
type topicsListWire struct {
	Topics []topicSummaryWireClient `json:"topics"`
}

type topicSummaryWireClient struct {
	TopicType   string `json:"topic_type"`
	ThreadCount int    `json:"thread_count"`
	TurnCount   int    `json:"turn_count"`
}

// ListTopics dials chat.topics_list and projects each row into
// pacmd.TopicSummary (Label <- TopicType).
func (c *cascadePATopicsClient) ListTopics(ctx context.Context) ([]pacmd.TopicSummary, error) {
	rpcC, err := c.rpcClient()
	if err != nil {
		return nil, err
	}
	var result topicsListWire
	if err := rpcC.Do(ctx, topics.MethodTopicsList, nil, &result); err != nil {
		return nil, err
	}
	out := make([]pacmd.TopicSummary, 0, len(result.Topics))
	for _, t := range result.Topics {
		out = append(out, pacmd.TopicSummary{Label: t.TopicType, ThreadCount: t.ThreadCount})
	}
	return out, nil
}

// threadsListParamsWire/threadsListResultWire/threadSummaryWireClient
// mirror rpc.go's threadsListParams/threadsListResult/threadSummaryWire.
type threadsListParamsWire struct {
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
}

type threadsListResultWire struct {
	Threads    []threadSummaryWireClient `json:"threads"`
	Page       int                       `json:"page"`
	PageSize   int                       `json:"page_size"`
	TotalCount int                       `json:"total_count"`
}

type threadSummaryWireClient struct {
	TopicType    string `json:"topic_type"`
	ID           string `json:"id"`
	TurnCount    int    `json:"turn_count"`
	LastActivity int64  `json:"last_activity"`
}

// ListThreads dials chat.threads_list. Slug and Title both project from
// TopicType: the topic engine's own ThreadStore keys one thread per
// topic type (thread_store.go) and has no separate title/slug concept,
// so TopicType is the only real, non-fabricated label available for
// either field.
func (c *cascadePATopicsClient) ListThreads(ctx context.Context, req pacmd.ThreadsListRequest) (pacmd.ThreadsListResult, error) {
	rpcC, err := c.rpcClient()
	if err != nil {
		return pacmd.ThreadsListResult{}, err
	}
	params := threadsListParamsWire{Page: req.Page, PageSize: req.PageSize}
	var result threadsListResultWire
	if err := rpcC.Do(ctx, topics.MethodThreadsList, params, &result); err != nil {
		return pacmd.ThreadsListResult{}, err
	}
	threads := make([]pacmd.ThreadSummary, 0, len(result.Threads))
	for _, t := range result.Threads {
		threads = append(threads, pacmd.ThreadSummary{
			ID: t.ID, Slug: t.TopicType, Title: t.TopicType, MessageCount: t.TurnCount,
		})
	}
	return pacmd.ThreadsListResult{
		Threads: threads, Page: result.Page, PageSize: result.PageSize, TotalCount: result.TotalCount,
	}, nil
}

// OpenThread dials the EXISTING chat.get_thread door, reusing
// getThreadParamsWire/getThreadResultWire -- already declared in this same
// package by cascadepa_tools_wiring.go over conversation's own exported
// Thread/Turn types -- rather than a second, redundant decode target. See
// this file's header for why OpenThread is not a third new RPC method.
func (c *cascadePATopicsClient) OpenThread(ctx context.Context, slug string) (pacmd.ThreadSummary, error) {
	rpcC, err := c.rpcClient()
	if err != nil {
		return pacmd.ThreadSummary{}, err
	}
	var result getThreadResultWire
	if err := rpcC.Do(ctx, conversation.MethodGetThread, getThreadParamsWire{ThreadID: slug}, &result); err != nil {
		return pacmd.ThreadSummary{}, err
	}
	return pacmd.ThreadSummary{
		ID: result.Thread.ID, Slug: slug, Title: result.Thread.Name, MessageCount: len(result.Turns),
	}, nil
}
