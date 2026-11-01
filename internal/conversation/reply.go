package conversation

// Purpose: Replier, the chat.reply pipeline: compose a thread's history
//   under the conversation budget, dispatch one streaming model call
//   through the in-process door at the thread's tier, publish deltas to
//   the ReplyHub, and persist the assistant turn exactly once per user turn
//   inside an intent/confirm effect record (contract:chat-reply).
// Inputs: ReplyConfig (every field required); a stream id from chat.reply_open.
// Outputs: ReplyResult, or a typed error. Partial output is never stored
//   and never reported as success: a cancel or a provider error records
//   FailEffect and returns the failure.
// Constraints: every model call (main stream, classify pre-stage,
//   summarize post-stage, summarizer) goes through tieredDoor, which sets
//   the thread tier from ThreadPrivacyFrom(ctx) and refuses a call without
//   one. No provider package and no internal/daemon import here.
// SPORT: internal.conversation.reply/ADDED (P1-CPA-16).

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/conductor"
	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const (
	// replyTaskClass is the main reply call's §5.16 task class.
	replyTaskClass = "chat"
	// replyEffectPrefix starts every chat.reply effect key.
	replyEffectPrefix = "chat.reply:"
	// maxReplyAttempts bounds the attempts one user turn may take.
	maxReplyAttempts = 64
)

var (
	errReplyConfig     = cascade.New(cascade.KindInvalidInput, "conversation: ReplyConfig requires every field")
	errReplyInProgress = cascade.New(cascade.KindConflict, "reply in progress")
	errReplyAttempts   = cascade.New(cascade.KindConflict, "conversation: reply attempt limit reached for this turn")
	errReplyTruncated  = cascade.New(cascade.KindUnavailable, "conversation: model stream ended without completing")
	errReplyOverBudget = cascade.New(cascade.KindInvalidInput, "conversation: the user turn alone exceeds the conversation budget")
	errDoorUntiered    = cascade.New(cascade.KindPolicyDenied, "conversation: model call carries no thread tier")
	errDoorTierDiffers = cascade.New(cascade.KindPolicyDenied, "conversation: model call tier differs from the thread tier")
)

// ReplyDoor is the in-process model door; *daemon.ConductorDoor satisfies it.
type ReplyDoor interface {
	Execute(ctx context.Context, req provider.ModelRequest) (provider.ModelResponse, error)
	ExecuteStream(ctx context.Context, req provider.ModelRequest) (<-chan provider.StreamEvent, func(), error)
}

// ReplyConfig wires a Replier. Every field is required; nil or zero is
// KindInvalidInput.
type ReplyConfig struct {
	Store   Store
	Journal JournalStore
	Door    ReplyDoor
	Counter provider.TokenCounter
	Effects *audit.EffectLog
	Hub     *ReplyHub
	Clock   Clock
	Budget  int
}

// ReplyRequest is chat.reply's input: thread and turn come from the
// stream binding chat.reply_open made.
type ReplyRequest struct {
	StreamID string
}

// ReplyResult is chat.reply's result. Replayed is true when the reply was
// already stored and no model call was made.
type ReplyResult struct {
	ThreadID        string `json:"thread_id"`
	TurnID          string `json:"turn_id"`
	AssistantTurnID string `json:"assistant_turn_id"`
	Replayed        bool   `json:"replayed"`
}

// Replier generates and persists assistant replies.
type Replier struct {
	cfg      ReplyConfig
	sql      *conversationStore
	door     tieredDoor
	composer *cascadecontext.Composer
	mu       sync.Mutex
	inflight map[string]struct{}
}

// NewReplier validates cfg and builds the composition pipeline: a
// Summarizer for overflowing slots, a classify pre-stage and a summarize
// post-stage, each calling the model only through the tiered door.
func NewReplier(cfg ReplyConfig) (*Replier, error) {
	if cfg.Store == nil || cfg.Journal == nil || cfg.Door == nil || cfg.Counter == nil ||
		cfg.Effects == nil || cfg.Hub == nil || cfg.Clock == nil || cfg.Budget <= 0 {
		return nil, errReplyConfig
	}
	cs, ok := cfg.Store.(*conversationStore)
	if !ok {
		return nil, errReplyStore
	}
	door := tieredDoor{next: cfg.Door}
	sum, err := cascadecontext.NewSummarizer(door, newSummaryCache(), cfg.Counter, cfg.Clock, nil)
	if err != nil {
		return nil, err
	}
	comp, err := cascadecontext.NewComposer(cfg.Counter, sum)
	if err != nil {
		return nil, err
	}
	pre, err := cascadecontext.NewPreStage(cascadecontext.PreStageClassify, door)
	if err != nil {
		return nil, err
	}
	post, err := cascadecontext.NewSummarizeStage(door, cfg.Counter)
	if err != nil {
		return nil, err
	}
	comp.SetPreStage(pre)
	comp.SetPostStage(post)
	return &Replier{cfg: cfg, sql: cs, door: door, composer: comp, inflight: map[string]struct{}{}}, nil
}

// Reply claims req.StreamID for the caller on ctx and answers the user
// turn it is bound to. The stream gets every delta and one terminal frame.
func (r *Replier) Reply(ctx context.Context, req ReplyRequest) (ReplyResult, error) {
	caller, err := requireCaller(ctx)
	if err != nil {
		return ReplyResult{}, err
	}
	b, err := r.cfg.Hub.claim(req.StreamID, caller)
	if err != nil {
		return ReplyResult{}, err
	}
	res, err := r.replyClaimed(ctx, b)
	r.cfg.Hub.finish(b.id, err)
	if err != nil {
		return ReplyResult{}, err
	}
	return res, nil
}

// replyClaimed holds the user turn's in-flight slot, then replays a stored
// reply or generates a new one.
func (r *Replier) replyClaimed(ctx context.Context, b replyBinding) (ReplyResult, error) {
	if !r.enter(b.turnID) {
		return ReplyResult{}, errReplyInProgress
	}
	defer r.leave(b.turnID)
	if err := r.checkTarget(ctx, b.threadID, b.turnID); err != nil {
		return ReplyResult{}, err
	}
	res := ReplyResult{ThreadID: b.threadID, TurnID: b.turnID}
	stored, ok, err := r.sql.replyFor(ctx, b.turnID)
	if err != nil {
		return ReplyResult{}, err
	}
	if ok {
		return r.replay(ctx, b, res, stored)
	}
	return r.generate(ctx, b, res)
}

// enter takes turnID's in-flight slot; false when another reply holds it.
func (r *Replier) enter(turnID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, busy := r.inflight[turnID]; busy {
		return false
	}
	r.inflight[turnID] = struct{}{}
	return true
}

func (r *Replier) leave(turnID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inflight, turnID)
}

// replay returns the stored reply, publishing its text, with no model call.
func (r *Replier) replay(ctx context.Context, b replyBinding, res ReplyResult, assistantID string) (ReplyResult, error) {
	segs, err := r.cfg.Store.ListSegments(ctx, assistantID)
	if err != nil {
		return ReplyResult{}, err
	}
	if text := joinSegmentContent(segs); text != "" {
		r.cfg.Hub.publish(b.id, text)
	}
	res.AssistantTurnID, res.Replayed = assistantID, true
	return res, nil
}

// generate composes, dispatches and persists one new reply inside an
// effect record: intent before the dispatch, confirm after the persist,
// failed on any cancel or error before it.
func (r *Replier) generate(ctx context.Context, b replyBinding, res ReplyResult) (ReplyResult, error) {
	tier, err := r.cfg.Store.ThreadPrivacy(ctx, b.threadID)
	if err != nil {
		return ReplyResult{}, err
	}
	tctx := conductor.ContextWithThreadPrivacy(ctx, conductor.ThreadPrivacy{ThreadID: b.threadID, Mode: tier})
	req, err := r.compose(tctx, b, tier)
	if err != nil {
		return ReplyResult{}, err
	}
	attempt, err := r.nextAttempt(ctx, b.turnID)
	if err != nil {
		return ReplyResult{}, err
	}
	key := replyEffectPrefix + b.turnID + ":" + strconv.Itoa(attempt)
	req.TaskID = "chat-reply-" + b.turnID + "-" + strconv.Itoa(attempt)
	h, err := r.cfg.Effects.BeginEffect(ctx, audit.EffectRequest{Kind: audit.KindExternalEffect,
		Actor: b.caller, Action: "chat.reply", Key: key, ParamsHash: audit.HashParams([]byte(b.turnID))})
	if err != nil {
		r.failEffect(ctx, h, err)
		return ReplyResult{}, err
	}
	text, err := r.stream(tctx, b.id, req)
	if err == nil {
		res.AssistantTurnID, err = r.persist(ctx, b, text)
	}
	if err != nil {
		r.failEffect(ctx, h, err)
		return ReplyResult{}, err
	}
	if _, err := r.cfg.Effects.ConfirmEffect(context.WithoutCancel(ctx), h, res.AssistantTurnID); err != nil {
		return ReplyResult{}, err
	}
	return res, nil
}

// failEffect records that the reply did not happen; a handle BeginEffect
// never returned has nothing to fail.
func (r *Replier) failEffect(ctx context.Context, h audit.EffectHandle, cause error) {
	if h.Key == "" {
		return
	}
	reason := "reply failed"
	if kind, ok := cascade.KindOf(cause); ok {
		reason = "reply failed: " + kind.String()
	}
	_, _ = r.cfg.Effects.FailEffect(context.WithoutCancel(ctx), h, reason)
}

// nextAttempt is the first attempt number no effect record holds for
// turnID: an attempt left unknown-outcome by a crash is never reused.
func (r *Replier) nextAttempt(ctx context.Context, turnID string) (int, error) {
	for n := 1; n <= maxReplyAttempts; n++ {
		_, found, err := r.cfg.Effects.EffectState(ctx, replyEffectPrefix+turnID+":"+strconv.Itoa(n))
		if err != nil {
			return 0, err
		}
		if !found {
			return n, nil
		}
	}
	return 0, errReplyAttempts
}

// RecoverPending marks every chat.reply intent left without a terminal
// record unknown-outcome. The daemon calls it at start.
func (r *Replier) RecoverPending(ctx context.Context) error {
	pending, err := r.cfg.Effects.PendingEffects(ctx)
	if err != nil {
		return err
	}
	for _, h := range pending {
		if !strings.HasPrefix(h.Key, replyEffectPrefix) {
			continue
		}
		if _, err := r.cfg.Effects.MarkUnknownOutcome(ctx, h); err != nil {
			return err
		}
	}
	return nil
}
