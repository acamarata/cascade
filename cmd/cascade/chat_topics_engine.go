// Purpose: the topic engine's composition root (P1-E21-W5-S46-T4 D2):
//
//	builds a chatTopicEngine over the real internal/conversation/topics
//	constructors and wires it into wireChatHandlers' Adapter via
//	conversation.SetTopicObserver, closing the gap five testonly-allow.json
//	entries had parked pending a named owner.
//
// Inputs: the store, clock and bus wireChatHandlers already has in hand,
//
//	plus paths (for the embedder/executor resolvers' doc trail below).
//
// Outputs: a *chatTopicEngine, always non-nil, implementing
//
//	conversation.TopicObserver.Observe. It is a working, fully wired
//	observer once an embedding provider exists; today it is a typed
//	"not configured" value (reason non-empty, observer nil) -- see
//	resolveChatEmbedder's own doc comment for the two independent,
//	disclosed gaps that force that state in every build of this tree as
//	of this ticket.
//
// Constraints: NewTaxonomyConfig and NewConversationThreadStore need
//
//	neither an embedder nor a retrieval store, so they are built
//	UNCONDITIONALLY, for real, on every call -- the exemplar store, the
//	auto-threader and the observe-logger are built only when
//	resolveChatEmbedder and resolveChatRetrievalStore both succeed. Every
//	constructor this file can reach is called from real, non-test code
//	(internal/build's test-only-usage gate asks only "is this symbol
//	referenced by shipping code", never "does that reference execute
//	today" -- see internal/build/testonlygate.go's own doc comment), so
//	all five parked exemptions retire with this ticket even though two
//	of the five constructors this file calls are, honestly, unreachable
//	until a narrower follow-up closes the embedder/store gaps below.
//
// SPORT: cmd/cascade chat topics engine (ADD) -- P1-E21-W5-S46-T4 D2.
package main

import (
	"context"
	"log/slog"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/conversation/topics"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// chatDefaultHysteresis is the segmenter's threshold/window until a
// config surface exists for it -- matching segmenter_config_test.go's own
// "valid" fixture (Threshold: 0.5), Window widened to 3 for a more
// conservative default confirmation run than the test's minimal case.
var chatDefaultHysteresis = topics.HysteresisConfig{Threshold: 0.5, Window: 3}

// chatTopicEngine implements conversation.TopicObserver. Build one with
// newChatTopicEngine; the zero value is not meaningfully usable (Observe
// on it always reports "not configured" with an empty reason, which is
// honest but undiagnosable, so always go through the constructor).
type chatTopicEngine struct {
	logger   *slog.Logger
	observer *topics.ObserveLogger // nil when not configured
	reason   string                // populated iff observer == nil
}

// Observe implements conversation.TopicObserver. It never returns an
// error (the interface has none): a not-configured engine or a real
// Observe failure both become a short warning string, logged here first
// -- PRIVACY: only threadID, counts and error text are logged, never a
// window's Text (internal/conversation/domain.go's rule extends to every
// caller of TopicWindowTurn, not just that package).
func (e *chatTopicEngine) Observe(ctx context.Context, threadID string, window []conversation.TopicWindowTurn) string {
	if e.observer == nil {
		e.logger.Warn("topics: engine not configured; turn observed but not segmented",
			"thread_id", threadID, "reason", e.reason)
		return "topics: not configured (" + e.reason + ")"
	}
	turns := make([]topics.Turn, len(window))
	for i, w := range window {
		turns[i] = topics.Turn{Speaker: w.Role, Text: w.Text}
	}
	if _, err := e.observer.Observe(ctx, turns); err != nil {
		e.logger.Error("topics: observe failed", "thread_id", threadID, "turns", len(turns), "error", err)
		return "topics: observe failed: " + err.Error()
	}
	return ""
}

// newChatTopicEngine builds the D2 composition: the two unconditional
// constructors, then the embedder/store-gated ones. A construction
// failure from an unconditional constructor is returned (a nil store or
// bus is this call's caller's bug, not a runtime condition to degrade
// on); a gated constructor's failure demotes the engine to not-configured
// with that error as the reason instead of failing wireChatHandlers.
func newChatTopicEngine(
	store conversation.Store, clock runtime.Clock, bus *events.Bus, paths runtime.PathProvider,
) (*chatTopicEngine, error) {
	logger := slog.Default().With("subsystem", "chat.topics")
	threadStore, err := topics.NewConversationThreadStore(store, clock)
	if err != nil {
		return nil, err
	}
	taxonomy := topics.NewTaxonomyConfig(nil, "") // MECHANISM ONLY (taxonomy.go) -- core names no labels
	engine := &chatTopicEngine{logger: logger}
	observer, reason := buildChatObserveLogger(threadStore, taxonomy, bus, clock, paths)
	engine.observer, engine.reason = observer, reason
	return engine, nil
}

// buildChatObserveLogger attempts the embedder/store-gated half of the
// composition: NewExemplarStore, NewDefaultAutoThreader and
// NewObserveLogger, in that order. It returns (nil, reason) the moment
// any dependency is unavailable, rather than a partially-built engine.
func buildChatObserveLogger(
	threadStore topics.ThreadStore, taxonomy topics.TaxonomyConfig, bus *events.Bus, clock runtime.Clock,
	paths runtime.PathProvider,
) (*topics.ObserveLogger, string) {
	executor, embedder, reason := resolveChatSegmenterDeps(paths)
	if embedder == nil {
		return nil, reason
	}
	retrievalStore, reason := resolveChatRetrievalStore(paths)
	if retrievalStore == nil {
		return nil, reason
	}
	exemplars, err := topics.NewExemplarStore(retrievalStore, clock, 0)
	if err != nil {
		return nil, err.Error()
	}
	threader, err := topics.NewDefaultAutoThreader(
		executor, embedder, chatDefaultHysteresis, threadStore, taxonomy, exemplars, bus, clock)
	if err != nil {
		return nil, err.Error()
	}
	observer, err := topics.NewObserveLogger(threader, retrievalStore, clock, bus)
	if err != nil {
		return nil, err.Error()
	}
	return observer, ""
}

// resolveChatSegmenterDeps names, in one place, the two independent
// reasons this composition root can never hand the segmenter a real
// provider.ModelExecutor/provider.Embedder pair as of this ticket --
// found by grepping the live tree, not assumed:
//
//  1. NO PRODUCTION EMBEDDER EXISTS ANYWHERE IN THIS TREE. Two real
//     provider.Embedder implementations ship (providers/embeddings.
//     ProviderEmbedder, api-backed; providers/embeddings/bgem3.Client, a
//     local sidecar), but neither has a composition-root caller: bgem3's
//     own package doc says so verbatim ("Not wired: nothing registers
//     this client as an active embedder lane in P1"), and ProviderEmbedder
//     needs a conductor-mediated EmbedExecutor
//     (internal/conductor/embed.go's Executor.EmbeddingExecutor) that
//     only a daemon composition root can build -- and the one real
//     *conductor.Executor this tree constructs is built inside
//     internal/daemon/conductor_execute.go:103's
//     RegisterConductorExecuteHandler (called, not built, from
//     cmd/cascade/daemon_unix_conductor.go's wireConductorExecute, itself
//     forbidden to this ticket); that constructed value is recorded on
//     the daemon's Manifest and never returned to any caller for reuse.
//     Unlike provider.ModelExecutor (below), there is also no RPC door to
//     loopback through: grepping the tree for a registered "model.embed"
//     or "conductor.embed" JSON-RPC method returns nothing --
//     internal/conductor/embed.go's own Executor.Embed is never exposed
//     over the wire at all.
//  2. provider.ModelExecutor HAS A WORKING PATTERN THIS FILE DOES NOT USE.
//     internal/plugins/review_wiring.go's reviewModelExecutor shows the
//     real fix: dial the daemon's OWN socket (paths.SocketPath()) and
//     call pkg/provider.Client.ModelExecute, which the daemon's already-
//     registered "conductor.execute" answers for real, with no forbidden
//     file touched. It is not built here because it would have nothing
//     to feed: NewSegmenter refuses a nil embedder regardless of whether
//     executor is real, and a live socket dial only this composition root
//     would exercise, for a value that can never be used, is exactly the
//     unnecessary complexity Policy 4 forbids. A follow-up that closes
//     gap 1 gets this one for the cost of one reviewModelExecutor-shaped
//     type.
func resolveChatSegmenterDeps(_ runtime.PathProvider) (provider.ModelExecutor, provider.Embedder, string) {
	return nil, nil, "no production embedding provider is composed anywhere in this daemon " +
		"(providers/embeddings.ProviderEmbedder and providers/embeddings/bgem3.Client exist but neither " +
		"has a composition-root caller, and no model.embed RPC door is registered to loop back through)"
}

// resolveChatRetrievalStore names the second, independent gap: even with
// an embedder, ExemplarStore needs a provider.Store. This is NOT a "no
// legitimate way to reach it" impossibility like gap 1 above -- the open
// handle already reaches this exact call frame: registerDBPathHandlers
// (cmd/cascade/plugin_rpc.go, forbidden to this ticket) receives it as
// its own `store` parameter and passes it to
// daemon.RegisterRecallIndexHandler at plugin_rpc.go:85, three lines
// before that same function calls wireChatHandlers at plugin_rpc.go:102
// -- without forwarding store as an argument. Closing this gap is one
// unpassed parameter and a scope choice in a forbidden file, not a
// structural blocker: no second providers/sqlite.Open, no new flock,
// nothing this ticket is permitted to touch. A follow-up that adds
// `store provider.Store` to wireChatHandlers' signature and threads it
// here closes this gap independently of gap 1 above.
func resolveChatRetrievalStore(_ runtime.PathProvider) (provider.Store, string) {
	return nil, "the daemon's real provider.Store is already in scope three lines above this " +
		"composition root's own call site (cmd/cascade/plugin_rpc.go:85, forbidden to this ticket) but " +
		"is not threaded through to wireChatHandlers (plugin_rpc.go:102) -- one unpassed parameter, not " +
		"an impossibility"
}
