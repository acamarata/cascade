package cmd

// Purpose (this file): `cascade chat --topics`, `--threads`, and
//   `--thread <slug>`'s "open" mode (U/S-46.T4): read-only surfaces over
//   the topic engine and thread domains built in S-45, reached through a
//   package-level Client seam exactly like chat.go's own OneShot/Stream
//   Client -- never a direct import of internal/conversation or
//   internal/conversation/topics (Art.10.2, plugins-providers-boundary).
//
// COMPLETION PASS (2026-09-21): chat.topics_list and chat.threads_list now
//   exist for real (internal/conversation/topics/rpc.go, registered from
//   cmd/cascade/chat_wiring.go), backed by NewConversationThreadStore over
//   the real conversation.Store -- that allow-list entry retires with this
//   pass. OpenThread reaches the existing chat.get_thread door instead of
//   a third new method (see internal/plugins/cascadepa_topics_wiring.go's
//   header). This file still builds the CLIENT side of the seam only --
//   never a direct import of internal/conversation or
//   internal/conversation/topics (Art.10.2, plugins-providers-boundary) --
//   mirroring chat.go's Client/SetClient/activeClient shape exactly; the
//   real implementation is injected by internal/plugins/
//   cascadepa_topics_wiring.go's init(), the same composition-root pattern
//   cascadepa_wiring.go already uses for SetClient.
//
// Inputs: chatOptions' topics/threads bool flags and page/pageSize ints
//   (chat.go), or a thread slug (opts.thread with an empty prompt).
// Outputs: text or --json rendering of TopicSummary/ThreadSummary data,
//   written to cc.OutOrStdout() only (never a bare os.Stdout reference,
//   matching chat.go's writeOneShotResult convention); a non-nil, typed
//   error on any daemon/client failure, propagated unchanged -- never
//   downgraded to an empty result.
// Constraints: privacy (S-44.T2): a local-only topic thread must never
//   surface through chat.topics_list/chat.threads_list. This file adds no
//   filtering logic of its own; the real RPC methods own that enforcement
//   server-side (internal/conversation/topics/rpc.go's own header) --
//   verified there is no existing chat.list_threads filter to "reuse" (a
//   contract-text deviation, recorded in that file), so a new,
//   fail-closed filter was built instead of a fabricated reuse.
//
// SPORT: plugin.cascade-pa:topics-cli (ADD) -- P1-E21-W5-S46-T4.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TopicSummary is one topic engine label plus how many threads currently
// classify under it -- chat --topics' per-row wire shape.
type TopicSummary struct {
	Label       string `json:"label"`
	ThreadCount int    `json:"thread_count"`
}

// ThreadSummary is one thread's listing/open-mode wire shape: title,
// slug, and message count, per the ticket's --threads acceptance
// criterion.
type ThreadSummary struct {
	ID           string `json:"id"`
	Slug         string `json:"slug"`
	Title        string `json:"title"`
	MessageCount int    `json:"message_count"`
}

// ThreadsListRequest is chat --threads' pagination input.
type ThreadsListRequest struct {
	Page     int
	PageSize int
}

// ThreadsListResult is chat --threads' paginated wire shape.
type ThreadsListResult struct {
	Threads    []ThreadSummary `json:"threads"`
	Page       int             `json:"page"`
	PageSize   int             `json:"page_size"`
	TotalCount int             `json:"total_count"`
}

// TopicsThreadsClient is the seam over the daemon's topic/thread listing
// surface: chat.topics_list and chat.threads_list are real, registered
// methods (internal/conversation/topics/rpc.go, wired from
// cmd/cascade/chat_wiring.go); OpenThread reaches the existing
// chat.get_thread door instead of a third new method (see
// internal/plugins/cascadepa_topics_wiring.go's header). A composition
// root injects the real implementation via SetTopicsThreadsClient; until
// it does, every call
// returns a typed, actionable KindUnavailable error.
type TopicsThreadsClient interface {
	// ListTopics returns every active topic with its current thread
	// count, or a typed error (e.g. KindUnavailable while the topic
	// engine's 7-day observe window has not yet produced a routable
	// state -- see internal/conversation/topics' ObserveLogger).
	ListTopics(ctx context.Context) ([]TopicSummary, error)
	// ListThreads returns one page of threads.
	ListThreads(ctx context.Context, req ThreadsListRequest) (ThreadsListResult, error)
	// OpenThread resolves slug to its summary, or a typed KindNotFound
	// error naming the slug when it does not exist.
	OpenThread(ctx context.Context, slug string) (ThreadSummary, error)
}

// unconfiguredTopicsThreadsClient is the default TopicsThreadsClient:
// every call fails with a typed, actionable KindUnavailable error naming
// the missing wiring -- the correct behavior of a real binary that has
// not called SetTopicsThreadsClient, matching chat.go's
// unconfiguredClient precedent exactly (never a blank/empty result).
type unconfiguredTopicsThreadsClient struct{}

var errTopicsClientUnconfigured = cascade.New(cascade.KindUnavailable,
	"cascade chat --topics/--threads: no daemon topic-engine adapter is wired into this binary; "+
		"start the daemon with `cascade daemon run` and ensure cascade-pa's topics/threads client wiring is configured")

func (unconfiguredTopicsThreadsClient) ListTopics(context.Context) ([]TopicSummary, error) {
	return nil, errTopicsClientUnconfigured
}

func (unconfiguredTopicsThreadsClient) ListThreads(context.Context, ThreadsListRequest) (ThreadsListResult, error) {
	return ThreadsListResult{}, errTopicsClientUnconfigured
}

func (unconfiguredTopicsThreadsClient) OpenThread(context.Context, string) (ThreadSummary, error) {
	return ThreadSummary{}, errTopicsClientUnconfigured
}

// topicsClientState guards the package-level TopicsThreadsClient seam so
// SetTopicsThreadsClient is safe under concurrent registration/test use,
// matching chat.go's clientState.
var topicsClientState struct {
	mu sync.RWMutex
	c  TopicsThreadsClient
}

// SetTopicsThreadsClient injects the real TopicsThreadsClient
// implementation. Called by the composition root
// (internal/plugins/cascadepa_topics_wiring.go's init()) now that
// chat.topics_list/chat.threads_list are wired for real; tests call it
// directly to inject a fake.
func SetTopicsThreadsClient(c TopicsThreadsClient) {
	topicsClientState.mu.Lock()
	topicsClientState.c = c
	topicsClientState.mu.Unlock()
}

// activeTopicsThreadsClient returns the configured TopicsThreadsClient,
// or unconfiguredTopicsThreadsClient{} if SetTopicsThreadsClient has
// never been called.
func activeTopicsThreadsClient() TopicsThreadsClient {
	topicsClientState.mu.RLock()
	defer topicsClientState.mu.RUnlock()
	if topicsClientState.c == nil {
		return unconfiguredTopicsThreadsClient{}
	}
	return topicsClientState.c
}

// runTopicsList services `cascade chat --topics`. jsonOut selects the
// --json envelope; a client error propagates unchanged (never swallowed
// into an empty listing -- the topic-engine-not-ready acceptance
// criterion).
func runTopicsList(ctx context.Context, cc *cobra.Command, jsonOut bool) error {
	topics, err := activeTopicsThreadsClient().ListTopics(ctx)
	if err != nil {
		return err
	}
	if jsonOut {
		return json.NewEncoder(cc.OutOrStdout()).Encode(struct {
			Topics []TopicSummary `json:"topics"`
		}{Topics: topics})
	}
	var b strings.Builder
	if len(topics) == 0 {
		b.WriteString("no active topics\n")
	}
	for _, t := range topics {
		fmt.Fprintf(&b, "%s\t%d thread(s)\n", t.Label, t.ThreadCount)
	}
	_, err = cc.OutOrStdout().Write([]byte(b.String()))
	return err
}

// runThreadsList services `cascade chat --threads`, paginated per page/
// pageSize.
func runThreadsList(ctx context.Context, cc *cobra.Command, jsonOut bool, page, pageSize int) error {
	res, err := activeTopicsThreadsClient().ListThreads(ctx, ThreadsListRequest{Page: page, PageSize: pageSize})
	if err != nil {
		return err
	}
	if jsonOut {
		return json.NewEncoder(cc.OutOrStdout()).Encode(res)
	}
	var b strings.Builder
	if len(res.Threads) == 0 {
		b.WriteString("no threads\n")
	}
	for _, th := range res.Threads {
		fmt.Fprintf(&b, "%s\t%s\t%d message(s)\n", th.Slug, th.Title, th.MessageCount)
	}
	fmt.Fprintf(&b, "page %d/%d\n", res.Page, pageCount(res))
	_, err = cc.OutOrStdout().Write([]byte(b.String()))
	return err
}

// pageCount derives the total page count from res's TotalCount/PageSize,
// never dividing by zero.
func pageCount(res ThreadsListResult) int {
	if res.PageSize <= 0 {
		return 1
	}
	n := (res.TotalCount + res.PageSize - 1) / res.PageSize
	if n < 1 {
		return 1
	}
	return n
}

// openThread resolves slug via the TopicsThreadsClient seam -- the
// "open" half of `cascade chat --thread <slug>` with no prompt. A typed
// KindNotFound error on an unknown slug propagates unchanged.
func openThread(ctx context.Context, slug string) (ThreadSummary, error) {
	return activeTopicsThreadsClient().OpenThread(ctx, slug)
}

// writeThreadSummary renders an opened thread's summary as --json output
// through cc's own output stream.
func writeThreadSummary(cc *cobra.Command, s ThreadSummary) error {
	return json.NewEncoder(cc.OutOrStdout()).Encode(s)
}

// writeThreadSummaryText renders an opened thread's summary as plain text
// -- chat.go's non-interactive parity path for `--thread <slug>` with no
// --json and no real TTY (06-FORGE-SPEC §5.8): the same data --json would
// carry, in the same key order writeOneShotResult's default form uses.
func writeThreadSummaryText(cc *cobra.Command, s ThreadSummary) error {
	var b strings.Builder
	fmt.Fprintf(&b, "thread: %s\nslug: %s\ntitle: %s\nmessages: %d\n", s.ID, s.Slug, s.Title, s.MessageCount)
	_, err := cc.OutOrStdout().Write([]byte(b.String()))
	return err
}
