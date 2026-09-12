package cascadepa

// Purpose: the in-chat companion to G/S-14.T3's `cascade memory review`
//
//	CLI surface — `/memory review next|approve|skip|forget` — so a user
//	can act on pending promotion candidates without leaving a
//	conversation. `next`/`approve`/`skip` are presenters over the real
//	memory.review.list/memory.review.act RPCs; `forget` is the distinct
//	memory.forget method (G/S-14.T4) — it is not a review.act action.
//
// Inputs: the raw text a chat turn carries, and the ReviewChatClient seam
//
//	a composition root injects.
//
// Outputs: HandleReviewChatCommand's (reply, matched, error) triple.
// Constraints: plugins/** may import pkg/** only, never internal/**
//
//	(Art.10.2); no promotion logic lives here — every action dispatches
//	straight to the audited API and this file interprets only the reply.
//
// SPORT: plugins/cascade-pa:review-chat (ADD) — P1-E22-W5-S47-T4.

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The four in-chat review verbs. forget is NOT a memory.review.act action
// (that enum is {approve, skip, defer, revert}); it dispatches to the
// distinct memory.forget method.
const (
	reviewVerbNext    = "next"
	reviewVerbApprove = "approve"
	reviewVerbSkip    = "skip"
	reviewVerbForget  = "forget"
)

// reviewChatTrigger is the in-chat command's leading grammar.
const reviewChatTrigger = "/memory review"

// ReviewChatCandidate is the plugin-local mirror of the fields of
// internal/memory's CandidateSummary this presenter shows: the candidate's
// canonical address, its taxonomy kind (shown as the candidate's source),
// how many distinct sessions observed it (shown as its staleness proxy —
// CandidateSummary carries no dedicated staleness timestamp), and how many
// references the ledger has counted (its hit count).
type ReviewChatCandidate struct {
	ID       string
	Kind     string
	Sessions int
	RefCount int
}

// ReviewChatListing is the plugin-local mirror of memory.review.list's
// pending section.
type ReviewChatListing struct {
	Pending []ReviewChatCandidate
}

// ReviewChatActResult is the plugin-local mirror of memory.review.act's
// output.
type ReviewChatActResult struct {
	Action  string
	Changed bool
}

// ReviewChatForgetResult is the plugin-local mirror of memory.forget's
// output, trimmed to what an in-chat confirmation needs.
type ReviewChatForgetResult struct {
	Forgotten bool
}

// ReviewChatClient is the seam cascade-pa's review-chat handler calls
// through. A composition root injects a real internal/client-backed
// implementation via SetReviewChatClient.
type ReviewChatClient interface {
	// List returns the pending section of the review queue
	// (memory.review.list, Section=pending). It writes nothing.
	List(ctx context.Context) (ReviewChatListing, error)
	// Act dispatches one action (approve or skip) on id to
	// memory.review.act.
	Act(ctx context.Context, id, action string) (ReviewChatActResult, error)
	// Forget dispatches id to the distinct memory.forget method.
	Forget(ctx context.Context, id string) (ReviewChatForgetResult, error)
}

// errReviewChatUnconfigured is the no-profile-context refusal, mirroring
// errSoulChatUnconfigured for the review-queue surface.
var errReviewChatUnconfigured = cascade.New(cascade.KindUnavailable,
	"cascade chat: /memory review has no review queue wired into this session; "+
		"start the daemon with `cascade daemon run` and ensure cascade-pa's "+
		"review-chat client wiring is configured")

type unconfiguredReviewChatClient struct{}

func (unconfiguredReviewChatClient) List(context.Context) (ReviewChatListing, error) {
	return ReviewChatListing{}, errReviewChatUnconfigured
}

func (unconfiguredReviewChatClient) Act(context.Context, string, string) (ReviewChatActResult, error) {
	return ReviewChatActResult{}, errReviewChatUnconfigured
}

func (unconfiguredReviewChatClient) Forget(context.Context, string) (ReviewChatForgetResult, error) {
	return ReviewChatForgetResult{}, errReviewChatUnconfigured
}

var reviewChatState struct {
	mu sync.RWMutex
	c  ReviewChatClient
}

// SetReviewChatClient injects the real ReviewChatClient. Called exactly
// once, by the composition root, before any in-chat `/memory review`
// trigger is handled. Tests call it directly to inject a stub.
func SetReviewChatClient(c ReviewChatClient) {
	reviewChatState.mu.Lock()
	reviewChatState.c = c
	reviewChatState.mu.Unlock()
}

func activeReviewChatClient() ReviewChatClient {
	reviewChatState.mu.RLock()
	defer reviewChatState.mu.RUnlock()
	if reviewChatState.c == nil {
		return unconfiguredReviewChatClient{}
	}
	return reviewChatState.c
}

// errReviewChatParse is the parse-failure refusal.
var errReviewChatParse = cascade.New(cascade.KindInvalidInput,
	"usage: /memory review next|approve <id>|skip <id>|forget <id>")

// parseReviewChatTrigger parses one chat line against the
// `/memory review` grammar. matched is false when the line does not begin
// with the trigger at all.
func parseReviewChatTrigger(text string) (verb, id string, matched bool, err error) {
	if !strings.HasPrefix(text, reviewChatTrigger) {
		return "", "", false, nil
	}
	rest := text[len(reviewChatTrigger):]
	if rest != "" && !strings.HasPrefix(rest, " ") {
		return "", "", false, nil
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", "", true, errReviewChatParse
	}
	verb = fields[0]
	switch verb {
	case reviewVerbNext:
		if len(fields) != 1 {
			return "", "", true, errReviewChatParse
		}
		return verb, "", true, nil
	case reviewVerbApprove, reviewVerbSkip, reviewVerbForget:
		if len(fields) != 2 {
			return "", "", true, errReviewChatParse
		}
		return verb, fields[1], true, nil
	default:
		return "", "", true, errReviewChatParse
	}
}

// HandleReviewChatCommand is the in-chat `/memory review` command's whole
// behavior. matched is false only when text is not this command at all.
func HandleReviewChatCommand(ctx context.Context, text string) (reply string, matched bool, err error) {
	verb, id, matched, err := parseReviewChatTrigger(text)
	if !matched {
		return "", false, nil
	}
	if err != nil {
		return "", true, err
	}

	client := activeReviewChatClient()
	switch verb {
	case reviewVerbNext:
		return reviewChatNext(ctx, client)
	case reviewVerbApprove, reviewVerbSkip:
		return reviewChatAct(ctx, client, id, verb)
	case reviewVerbForget:
		return reviewChatForget(ctx, client, id)
	}
	return "", true, errReviewChatParse
}

// reviewChatNext presents the first pending candidate, or a "nothing
// pending" acknowledgement — never an error — for an empty queue.
func reviewChatNext(ctx context.Context, client ReviewChatClient) (string, bool, error) {
	listing, err := client.List(ctx)
	if err != nil {
		return "", true, err
	}
	if len(listing.Pending) == 0 {
		return "nothing pending", true, nil
	}
	c := listing.Pending[0]
	return fmt.Sprintf("pending: %s (source: %s, sessions: %d, hits: %d)",
		c.ID, c.Kind, c.Sessions, c.RefCount), true, nil
}

// reviewChatAct dispatches approve/skip to memory.review.act.
func reviewChatAct(ctx context.Context, client ReviewChatClient, id, verb string) (string, bool, error) {
	result, err := client.Act(ctx, id, verb)
	if err != nil {
		return "", true, err
	}
	changed := "no change"
	if result.Changed {
		changed = "applied"
	}
	return fmt.Sprintf("%s %s: %s", verb, id, changed), true, nil
}

// reviewChatForget dispatches to the distinct memory.forget method.
func reviewChatForget(ctx context.Context, client ReviewChatClient, id string) (string, bool, error) {
	result, err := client.Forget(ctx, id)
	if err != nil {
		return "", true, err
	}
	if !result.Forgotten {
		return fmt.Sprintf("forget %s: already forgotten", id), true, nil
	}
	return fmt.Sprintf("forget %s: forgotten", id), true, nil
}
