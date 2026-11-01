// Purpose: the topic engine's composition root (P1-E21-W5-S46-T4 D2,
//
//	turned on by P1-CPA-12): builds a chatTopicEngine over the real
//	internal/conversation/topics constructors and wires it into
//	wireChatHandlers' Adapter via conversation.SetTopicObserver.
//
// Inputs: the conversation store, clock and bus wireChatHandlers holds,
//
//	and paths, from which chatTopicDepsFor reads [topics], the process
//	embedder Selection, the conductor door and the lane registry.
//
// Outputs: a *chatTopicEngine, always non-nil, implementing
//
//	conversation.TopicObserver.Observe. It files topics only when a
//	classifier lane is configured, the Selection has an embedder and a
//	retrieval provider.Store is in hand; otherwise it is a typed "not
//	configured" value whose reason names the missing piece.
//
// Constraints: every Observe reads the thread's own privacy tier and skips,
//
//	with a named reason and no model call, when the tier is unreadable,
//	when the embedder Selection (the SAME pointer the recall vector leg
//	holds) does not admit it, or when the classifier lane's locality does
//	not admit it (conductor.SensitivityAllows, no restricted permit). An
//	admitted call carries the tier on its ctx (ContextWithDoorSensitivity
//	and ContextWithThreadPrivacy), which chatModelExecutor stamps on
//	ModelRequest.Sensitivity and the lane embedder reads. Only threadID,
//	counts, tiers and error text are logged, never a turn's text.
//
// SPORT: cmd/cascade chat topics engine (ADD) -- P1-E21-W5-S46-T4 D2.
package main

import (
	"context"
	"log/slog"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/conversation/topics"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/retrieval/embedsel"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// chatDefaultHysteresis is the segmenter's threshold/window until a
// config surface exists for it.
var chatDefaultHysteresis = topics.HysteresisConfig{Threshold: 0.5, Window: 3}

// topicObserver is the observe-or-apply step (*topics.ObserveLogger).
type topicObserver interface {
	Observe(ctx context.Context, turns []topics.Turn) (topics.ObserveResult, error)
}

// threadTierReader reads a thread's privacy tier (conversation.Store).
type threadTierReader interface {
	ThreadPrivacy(ctx context.Context, threadID string) (provider.SensitivityTier, error)
}

// chatTopicEngine implements conversation.TopicObserver. Build one with
// newChatTopicEngine; a zero value reports "not configured".
type chatTopicEngine struct {
	logger    *slog.Logger
	observer  topicObserver // nil when not configured
	reason    string        // populated iff observer == nil
	tiers     threadTierReader
	selection *embedsel.Selection
	lane      string
	laneType  laneTypeFunc
}

// Observe implements conversation.TopicObserver. It never returns an
// error: a skip, a not-configured engine or an Observe failure each become
// a short warning string, logged here first.
func (e *chatTopicEngine) Observe(ctx context.Context, threadID string, window []conversation.TopicWindowTurn) string {
	if e.observer == nil {
		e.logger.Warn("topics: engine not configured; turn observed but not segmented",
			"thread_id", threadID, "reason", e.reason)
		return "topics: not configured (" + e.reason + ")"
	}
	tier, skip := e.admit(ctx, threadID)
	if skip != "" {
		e.logger.Info("topics: turn skipped", "thread_id", threadID, "reason", skip)
		return "topics: skipped (" + skip + ")"
	}
	ctx = conductor.ContextWithDoorSensitivity(ctx, tier)
	ctx = conductor.ContextWithThreadPrivacy(ctx, conductor.ThreadPrivacy{ThreadID: threadID, Mode: tier})
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

// admit returns the thread's tier, or the named reason the turn must not
// reach the embedder or the classifier lane.
func (e *chatTopicEngine) admit(ctx context.Context, threadID string) (provider.SensitivityTier, string) {
	if e.tiers == nil {
		return 0, "thread tier unreadable: no tier reader"
	}
	tier, err := e.tiers.ThreadPrivacy(ctx, threadID)
	if err != nil || !tier.Valid() {
		reason := "thread tier unreadable"
		if err != nil {
			reason += ": " + err.Error()
		}
		return 0, reason
	}
	if e.selection == nil || !e.selection.Admits(tier) {
		return 0, "embedder selection does not admit tier " + tier.String()
	}
	if e.laneType == nil {
		return 0, "classifier lane " + e.lane + " locality unknown"
	}
	lt, err := e.laneType(ctx, e.lane)
	if err != nil {
		return 0, "classifier lane " + e.lane + " unresolved: " + err.Error()
	}
	if !conductor.SensitivityAllows(tier, lt, false) {
		return 0, "classifier lane " + e.lane + " (" + lt.String() + ") does not admit tier " + tier.String()
	}
	return tier, ""
}

// newChatTopicEngine builds the production engine for paths.
func newChatTopicEngine(
	store conversation.Store, clock runtime.Clock, bus *events.Bus, paths runtime.PathProvider,
) (*chatTopicEngine, error) {
	return newChatTopicEngineWith(store, clock, bus, chatTopicDepsFor(paths, clock))
}

// newChatTopicEngineWith builds the engine over deps. A failure of the
// unconditional thread store is returned; a missing or failing gated
// dependency demotes the engine to not-configured with that reason.
func newChatTopicEngineWith(
	store conversation.Store, clock runtime.Clock, bus *events.Bus, deps chatTopicDeps,
) (*chatTopicEngine, error) {
	threadStore, err := topics.NewConversationThreadStore(store, clock)
	if err != nil {
		return nil, err
	}
	engine := &chatTopicEngine{
		logger: slog.Default().With("subsystem", "chat.topics"), tiers: store,
		selection: deps.selection, lane: deps.settings.ClassifierLane, laneType: deps.laneType,
	}
	observer, reason := buildChatObserveLogger(threadStore, deps.taxonomy, bus, clock, deps)
	if observer != nil {
		engine.observer = observer
	}
	engine.reason = reason
	return engine, nil
}

// chatTopicsReadiness names the first missing piece of deps, or "" when
// the engine can be fully built.
func chatTopicsReadiness(deps chatTopicDeps) string {
	switch {
	case deps.settingsErr != nil:
		return "[topics] config refused: " + deps.settingsErr.Error()
	case !deps.settings.Enabled():
		return "no classifier lane configured ([topics] classifier_lane is unset)"
	case deps.selection == nil || deps.selection.Embedder() == nil:
		return "no embedder selected: " + selectionReason(deps.selection)
	case deps.retrieval == nil:
		return deps.storeReason
	default:
		return ""
	}
}

// selectionReason is the Selection's own error text.
func selectionReason(s *embedsel.Selection) string {
	if s == nil || s.Err() == nil {
		return "retrieval.embedding is not configured"
	}
	return s.Err().Error()
}

// buildChatObserveLogger builds NewExemplarStore, NewDefaultAutoThreader
// and NewObserveLogger over deps, or returns (nil, reason).
func buildChatObserveLogger(
	threadStore topics.ThreadStore, taxonomy topics.TaxonomyConfig, bus *events.Bus, clock runtime.Clock,
	deps chatTopicDeps,
) (*topics.ObserveLogger, string) {
	if reason := chatTopicsReadiness(deps); reason != "" {
		return nil, reason
	}
	exemplars, err := topics.NewExemplarStore(deps.retrieval, clock, 0)
	if err != nil {
		return nil, err.Error()
	}
	executor := chatModelExecutor{door: deps.door, lane: deps.settings.ClassifierLane, model: deps.settings.ClassifierModel}
	threader, err := topics.NewDefaultAutoThreader(
		executor, deps.selection.Embedder(), chatDefaultHysteresis, threadStore, taxonomy, exemplars, bus, clock)
	if err != nil {
		return nil, err.Error()
	}
	observer, err := topics.NewObserveLogger(threader, deps.retrieval, clock, bus)
	if err != nil {
		return nil, err.Error()
	}
	return observer, ""
}
