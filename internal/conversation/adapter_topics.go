package conversation

// Purpose: TopicObserver, the narrow post-commit seam handleAppendTurn
//   drives after a turn is durably stored and echoed (P1-E21-W5-S46-T4
//   D2): builds the thread's current turn window and hands it to
//   whatever topic engine the composition root wired in, then folds the
//   result into chat.append_turn's own response as a non-fatal field.
// Inputs: the just-appended Turn and its Segments (already in
//   handleAppendTurn's hands) plus a.store's existing history for the
//   same thread.
// Outputs: appendTurnResult, always populated -- TopicWarning is empty on
//   success or when no TopicObserver is wired (T2/embedded default), and
//   a short, content-free message otherwise.
// Constraints: THE APPEND NEVER FAILS BECAUSE OF THIS. TopicObserver.
//   Observe has no error return by design (its doc comment says why), so
//   there is nothing here to propagate as a hard failure -- matching the
//   T0 decision's "fail-closed for the ENGINE, not for chat". PRIVACY
//   (domain.go's own rule): TopicWindowTurn.Text is flattened
//   Segment.Content, so it crosses this package's boundary into whatever
//   composed the TopicObserver -- that composition root, never this
//   file, owns not logging it. This file never logs a window's Text.
// SPORT: internal.conversation.adapter (CHANGED -- topic engine hook,
//   P1-E21-W5-S46-T4).

import "context"

// TopicWindowTurn is one turn of the window a TopicObserver receives:
// role name and flattened text content. Deliberately its own shape,
// independent of internal/conversation/topics.Turn, so this package
// never imports topics -- topics already imports this package for its
// ThreadStore (internal/conversation/topics/thread_store.go's
// NewConversationThreadStore), so the reverse import would cycle. The
// daemon composition root (cmd/cascade/chat_wiring.go and its
// chatTopicEngine) maps this into topics.Turn.
type TopicWindowTurn struct {
	// Role is the speaking Turn's Role, as its string value.
	Role string
	// Text is the turn's segments, flattened by joinSegmentContent
	// (adapter_wire.go) -- the same flattening chat.search already
	// applies to a matched turn's segments.
	Text string
}

// TopicObserver is handleAppendTurn's post-commit hook into the topic
// engine (P1-E21-W5-S46-T4 D2). Observe receives threadID and the
// thread's full current turn window (every turn appended so far,
// including the one just committed, oldest first) and returns a short,
// human-readable warning to surface on the RPC response -- empty on
// success.
//
// Observe never returns an error BY DESIGN: a topic-engine failure is
// this call's problem to log and summarize, never chat.append_turn's
// problem to fail on, because the turn is already durably stored by the
// time finishAppend calls this. An implementation that cannot even start
// (no embedder configured) reports that as a warning string too, not a
// panic or a swallowed no-op.
type TopicObserver interface {
	Observe(ctx context.Context, threadID string, window []TopicWindowTurn) (warning string)
}

// SetTopicObserver wires o into handleAppendTurn. Nil (the default after
// NewAdapter) means "no topic engine composed" -- matching SetJournal/
// SetScrub's identical optional-seam pattern in adapter.go -- and
// finishAppend then skips the call entirely rather than invoking a nil
// interface.
func (a *Adapter) SetTopicObserver(o TopicObserver) { a.topics = o }

// finishAppend builds handleAppendTurn's response, running the wired
// TopicObserver (if any) over the thread's current window first. It is
// the single call site both of handleAppendTurn's return points use, so
// the observe hook fires on the embedded-mode SSE short-circuit exactly
// like it does on the ordinary path -- the turn is committed either way.
func (a *Adapter) finishAppend(ctx context.Context, turn Turn, segments []Segment) appendTurnResult {
	return appendTurnResult{
		ThreadID:     turn.ThreadID,
		TurnID:       turn.ID,
		Seq:          turn.Seq,
		TopicWarning: a.observeTopics(ctx, turn, segments),
	}
}

// observeTopics builds the thread's full turn window and hands it to
// a.topics. A nil a.topics (no engine composed) returns "" without a
// store round trip; a window-build failure (a store read error) is
// itself reported as the warning rather than silently dropped, since
// Observe has nothing else to report it through.
func (a *Adapter) observeTopics(ctx context.Context, turn Turn, segments []Segment) string {
	if a.topics == nil {
		return ""
	}
	window, err := a.buildTopicWindow(ctx, turn, segments)
	if err != nil {
		return "topics: could not build turn window: " + err.Error()
	}
	return a.topics.Observe(ctx, turn.ThreadID, window)
}

// buildTopicWindow lists every turn of turn.ThreadID (handleAppendTurn's
// own store write already committed turn above, so ListTurns's result
// already includes it) and flattens each one's segments into a
// TopicWindowTurn, oldest first. The just-appended turn reuses the
// segments finishAppend already has in hand instead of a redundant
// ListSegments round trip for the one turn this call always already
// knows.
func (a *Adapter) buildTopicWindow(ctx context.Context, turn Turn, segments []Segment) ([]TopicWindowTurn, error) {
	all, err := a.store.ListTurns(ctx, turn.ThreadID)
	if err != nil {
		return nil, err
	}
	window := make([]TopicWindowTurn, 0, len(all))
	for _, t := range all {
		if t.ID == turn.ID {
			window = append(window, TopicWindowTurn{Role: string(t.Role), Text: joinSegmentContent(segments)})
			continue
		}
		segs, err := a.store.ListSegments(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		window = append(window, TopicWindowTurn{Role: string(t.Role), Text: joinSegmentContent(segs)})
	}
	return window, nil
}
