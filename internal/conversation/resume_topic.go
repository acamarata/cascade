package conversation

// Purpose (this file): the thread-resolution half of Resumer: given the
//   thread ids a request resumes (a topic id, or the threads the query path
//   resolved), refuse any whose tier is more restrictive than the
//   destination's, else build the history Slots the Composer assembles.
//   Split out of resume.go under Art.10.3's 300-line cap.
// Inputs: thread ids, the destination session's stored §5.16 tier
//   (IntoTier, resolved daemon-side by adapter_resume.go) and the caller's
//   thread view (ResumeRequest.Admits; nil admits every thread).
// Outputs: the contributing threads' joined tier (provider.JoinSensitivity)
//   and ordered Slots (each thread's newest turn first, threads in the
//   order given), or a typed error.
// Constraints: IDENTITY. A U/S-45 topic IS a conversation.Thread
//   (internal/conversation/topics wraps this package's Store and imports
//   it, so the reverse import would cycle): a topic id is a thread id, used
//   directly. PRIVACY: the source-versus-target rule is the strict order
//   local-only > restricted > internal > public
//   (provider.SensitivityTier.MoreRestrictiveThan). A source more
//   restrictive than the target is refused whole, before any turn is read;
//   an equal or less restrictive source is admitted. This also refuses
//   restricted content into an internal thread, whose egress classes (the
//   Telegram bridge admits internal) would otherwise carry it off the
//   machine.
// SPORT: internal.conversation.resume/ADDED (P1-E22-W5-S47-T2).

import (
	"context"

	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/pkg/provider"
)

// resolveThreads resolves threadIDs to their turn histories, enforcing the
// caller's view and the source-versus-target rule per thread at the
// cheapest point (one ThreadPrivacy read) before any history is fetched; one
// refused thread refuses the whole resume with nothing composed. An unknown
// id is KindNotFound. The returned tier is the join of every thread's tier.
func (r *Resumer) resolveThreads(
	ctx context.Context, threadIDs []string, intoTier provider.SensitivityTier, admits func(string) bool,
) (provider.SensitivityTier, []cascadecontext.Slot, error) {
	tiers := make([]provider.SensitivityTier, 0, len(threadIDs))
	for _, threadID := range threadIDs {
		tier, err := r.checkThread(ctx, threadID, intoTier, admits)
		if err != nil {
			return 0, nil, err
		}
		tiers = append(tiers, tier)
	}
	var slots []cascadecontext.Slot
	for _, threadID := range threadIDs {
		threadSlots, err := r.buildHistorySlots(ctx, threadID)
		if err != nil {
			return 0, nil, err
		}
		slots = append(slots, threadSlots...)
	}
	return provider.JoinSensitivity(tiers...), slots, nil
}

// checkThread admits one source thread: it must exist, be in the caller's
// view (a thread outside it reads as the same not-found as an unknown id,
// so a principal cannot probe other scopes) and be no more restrictive than
// the destination. The tier read here and the history read in
// buildHistorySlots are separate store reads; they agree because a marker is
// write-once (privacy.go: SetThreadPrivacy), so a thread cannot be re-marked
// between them.
func (r *Resumer) checkThread(
	ctx context.Context, threadID string, intoTier provider.SensitivityTier, admits func(string) bool,
) (provider.SensitivityTier, error) {
	if _, ok, err := r.store.GetThread(ctx, threadID); err != nil {
		return 0, err
	} else if !ok || (admits != nil && !admits(threadID)) {
		return 0, errResumeTopicNotFound(threadID)
	}
	tier, err := r.store.ThreadPrivacy(ctx, threadID)
	if err != nil {
		return 0, err
	}
	if tier.MoreRestrictiveThan(intoTier) {
		return 0, errResumePrivacyRefused(threadID, tier, intoTier)
	}
	return tier, nil
}

// buildHistorySlots lists topicID's turns and builds one SlotKindHistory
// Slot per non-empty turn, NEWEST FIRST (reverse of ListTurns' own Seq
// order): Compose (composer_core.go) fills slots in the caller's
// declared PRIORITY order, so putting the most recent exchange first is
// what makes an over-budget resume keep the newest context and trim/drop
// the oldest, rather than the reverse. Label carries only "turn:<id>" --
// TurnID is content-addressed (domain.go's NewTurnID), never conversation
// text, so it is safe in a BudgetTrimEvent per composer_types.go's own
// Label rule.
func (r *Resumer) buildHistorySlots(ctx context.Context, threadID string) ([]cascadecontext.Slot, error) {
	turns, err := r.store.ListTurns(ctx, threadID)
	if err != nil {
		return nil, err
	}
	slots := make([]cascadecontext.Slot, 0, len(turns))
	for i := len(turns) - 1; i >= 0; i-- {
		turn := turns[i]
		segs, err := r.store.ListSegments(ctx, turn.ID)
		if err != nil {
			return nil, err
		}
		content := joinSegmentContent(segs)
		if content == "" {
			continue
		}
		slots = append(slots, cascadecontext.Slot{
			Kind:    cascadecontext.SlotKindHistory,
			Label:   "turn:" + turn.ID,
			Content: content,
		})
	}
	return slots, nil
}
