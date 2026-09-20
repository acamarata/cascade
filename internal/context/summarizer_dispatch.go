package context

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: the model-facing half of the rolling summarizer: building the
//   provider.ModelRequest that carries the task_class = summarize contract
//   (06-FORGE-SPEC.md §5.16: cheap-lane affinity) and FOLDS a prior summary
//   into the new content window, measuring every window and every response
//   with the injected provider.TokenCounter, and the Summarize adapter that
//   satisfies composer_types.go's SummarizerGetter seam (T1).
// Inputs: an entityID, a Granularity, the prior summary (may be empty) and
//   the caller's current source content; a non-nil provider.ModelExecutor.
// Outputs: the model's text output, a substitute Slot, or a typed error.
// Constraints: production code in this package NEVER imports
//   internal/conductor (T1 and T2 share no compile-time dependency beyond
//   the SummarizerGetter interface, and this package takes the same stance
//   toward conductor). TaskClass is therefore the literal below;
//   summarizer_dispatch_test.go asserts it equals
//   conductor.TaskClassSummarize, so the literal cannot drift from the
//   taxonomy without a red test.
//
//   SENSITIVITY IS INHERITED, NOT DECLARED HERE. The request sets NO Policy
//   and leaves Sensitivity at its zero value, which IS
//   provider.SensitivityRestricted by declaration (R-21.264) and is what
//   conductor.ResolveSensitivity resolves an unset field to. That is
//   deliberate. The conductor's FILTER 0 (internal/conductor/privacy.go)
//   applies the calling thread's privacy_mode, carried on the request
//   context, to EVERY Select regardless of what the request itself says, so
//   inheritance is enforced by the router and not by this caller. What this
//   caller owes is only that it never LOOSENS the inherited mode -- which a
//   Sensitivity left at restricted and an absent Policy together guarantee
//   (Policy.ExternalAllowed false is the narrower setting: it forces
//   local-only placement). An earlier revision set
//   Policy{ExternalAllowed: true} while its comment claimed fail-closed; it
//   did the opposite of what it said and is gone.
// SPORT: context-engine/summarizer-dispatch (ADD, P1-E21-W5-S46-T2).

// The §5.16 taxonomy row for the summarize task class, transcribed: class
// name, Reasoning "low", CtxK 32. The Requirements values are advisory --
// Router.filterCapability gates lanes on RequiredCapabilities, not on
// Requirements -- so setting them never excludes a lane; they exist for
// fidelity to the row.
const (
	summarizeTaskClass     = "summarize"
	summarizeReasoning     = "low"
	summarizeContextTokens = 32000
)

// The two prompt templates. The rolling one is used whenever a prior
// summary exists: the model is asked to UPDATE that summary with the new
// material rather than to summarize the window on its own, which is what
// makes the summary roll forward instead of being rebuilt from whatever
// slice of history happens to fit the window.
const (
	summarizePromptRolling = "You are maintaining a rolling %s summary. Update the PRIOR SUMMARY " +
		"below so it also covers the NEW MATERIAL that follows it, preserving facts and decisions " +
		"from both. Answer with the updated summary only, at most %d tokens.\n\n" +
		"PRIOR SUMMARY:\n%s\n\nNEW MATERIAL:\n%s"
	summarizePromptInitial = "Write a %s summary of the CONTENT below, preserving key facts and " +
		"decisions. Answer with the summary only, at most %d tokens.\n\nCONTENT:\n%s"
)

// var _ SummarizerGetter = (*Summarizer)(nil) asserts *Summarizer satisfies
// composer_types.go's (T1) dependency-injection seam.
var _ SummarizerGetter = (*Summarizer)(nil)

// buildModelRequest constructs the ModelRequest a regeneration dispatches:
// task_class summarize, the new content truncated to level's code-default
// token window (keeping the most recent content), and prior folded in when
// there is one.
func (s *Summarizer) buildModelRequest(ctx context.Context, entityID string, level Granularity, prior, content string) (provider.ModelRequest, error) {
	id, err := cascade.NewID()
	if err != nil {
		return provider.ModelRequest{}, cascade.Wrap(cascade.KindInternal, err, "context: summarizer: minting task id")
	}
	windowed, err := truncateTailTokens(ctx, s.counter, content, DefaultWindowTokens(level))
	if err != nil {
		return provider.ModelRequest{}, errSummarizerDependency(err, "context: summarizer: measuring the source window")
	}
	return provider.ModelRequest{
		TaskID:    "summarizer-" + entityID + "-" + string(id),
		TaskClass: summarizeTaskClass,
		Inputs:    []provider.ChatMessage{{Role: "user", Content: summarizePrompt(level, prior, windowed)}},
		Requirements: provider.Requirements{
			Reasoning: summarizeReasoning,
			Context:   summarizeContextTokens,
		},
	}, nil
}

// summarizePrompt renders the rolling template when prior is non-empty and
// the initial one otherwise.
func summarizePrompt(level Granularity, prior, windowed string) string {
	bound := MaxSummaryTokens(level)
	if prior == "" {
		return fmt.Sprintf(summarizePromptInitial, level.String(), bound, windowed)
	}
	return fmt.Sprintf(summarizePromptRolling, level.String(), bound, prior, windowed)
}

// checkOutputSize refuses a model response longer than level's own summary
// bound. An over-long "summary" is a failed summarization, not a large one:
// persisting it would put a block the composer cannot afford into the store
// and serve it on every later compose.
func (s *Summarizer) checkOutputSize(ctx context.Context, level Granularity, output string) error {
	bound := MaxSummaryTokens(level)
	n, err := s.counter.Count(ctx, output)
	if err != nil {
		return errSummarizerDependency(err, "context: summarizer: measuring the model response")
	}
	if n > bound {
		return errSummarizerOversizeOutput(level, n, bound)
	}
	return nil
}

// dispatchExecute calls executor.Execute and returns the model's text
// output, or a typed error wrapping whatever Kind the executor's own
// failure already carries (errSummarizerDependency).
func dispatchExecute(ctx context.Context, executor provider.ModelExecutor, req provider.ModelRequest) (string, error) {
	resp, err := executor.Execute(ctx, req)
	if err != nil {
		return "", errSummarizerDependency(err, "context: summarizer: model.execute dispatch failed")
	}
	return resp.Output, nil
}

// Summarize implements composer_types.go's SummarizerGetter seam (T1).
// slot.Label is the entity id (Label is already documented as a short,
// caller-chosen, content-free identifier). The GRANULARITY is chosen from
// remainingBudget by SelectGranularity: the composer calls this precisely
// when a slot did not fit, so how much room is left is the one fact that
// decides how compressed a substitute has to be. sourceVersion is a content
// hash, so an unchanged slot reuses its cached summary and a changed one
// regenerates -- the determinism Compose's own contract requires.
//
// A Stale or Failed outcome is returned as an ERROR, never as content:
// Compose's resolveOverflow treats a summarizer error as "no substitute
// available" and drops the slot whole with a BudgetTrimEvent recording why,
// which is the fail-closed path. Serving a stale summary here would instead
// put content describing a PREVIOUS version of the source into an assembly
// that claims to describe the current one.
func (s *Summarizer) Summarize(ctx context.Context, slot Slot, remainingBudget int) (Slot, error) {
	if slot.Label == "" {
		return Slot{}, errSummarizerEmptyEntityID()
	}
	level := SelectGranularity(remainingBudget)
	outcome, err := s.GetSummary(ctx, slot.Label, level, slot.Content, contentVersion(slot.Content))
	if err != nil {
		return Slot{}, err
	}
	if err := refuseDegradedOutcome(outcome); err != nil {
		return Slot{}, err
	}
	if outcome.Record.Content == "" {
		return Slot{}, cascade.New(cascade.KindUnavailable, "context: summarizer: no substitute available")
	}
	content, err := truncateHeadTokens(ctx, s.counter, outcome.Record.Content, remainingBudget)
	if err != nil {
		return Slot{}, errSummarizerDependency(err, "context: summarizer: measuring the substitute")
	}
	return Slot{Kind: slot.Kind, Label: slot.Label, Content: content}, nil
}

// contentVersion returns a short, deterministic, content-derived version
// tag: identical content always yields the same tag, so an unchanged Slot
// reuses its cached summary instead of triggering a new model.execute call
// on every Compose.
func contentVersion(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:16]
}

// truncateTailTokens returns the longest SUFFIX of s that fits maxTokens as
// measured by counter: the most recent source content survives, which is
// what a recency-bounded window means.
func truncateTailTokens(ctx context.Context, counter provider.TokenCounter, s string, maxTokens int) (string, error) {
	r := []rune(s)
	k, err := longestFittingPrefixLen(ctx, counter, len(r), maxTokens, func(n int) string {
		return string(r[len(r)-n:])
	})
	if err != nil {
		return "", err
	}
	return string(r[len(r)-k:]), nil
}

// truncateHeadTokens returns the longest PREFIX of s that fits maxTokens,
// cut back to a sentence boundary where one is near enough to the end. A
// summary reads top-down, so its opening is the part worth keeping; and a
// summary cut mid-sentence reads as corrupted output rather than as a
// shortened summary.
func truncateHeadTokens(ctx context.Context, counter provider.TokenCounter, s string, maxTokens int) (string, error) {
	r := []rune(s)
	k, err := longestFittingPrefixLen(ctx, counter, len(r), maxTokens, func(n int) string {
		return string(r[:n])
	})
	if err != nil {
		return "", err
	}
	if k == len(r) {
		return s, nil
	}
	return cutAtSentenceBoundary(string(r[:k])), nil
}

// cutAtSentenceBoundary trims s back to its last sentence terminator, but
// only when that terminator is in the second half of s: giving up more than
// half the room to end on a full stop costs more than the ragged edge does.
func cutAtSentenceBoundary(s string) string {
	idx := strings.LastIndexAny(s, ".!?\n")
	if idx < 0 || idx+1 < len(s)/2 {
		return s
	}
	return s[:idx+1]
}

// longestFittingPrefixLen binary-searches the largest n in [0, total] for
// which pick(n) measures at most maxTokens. It assumes what every counter in
// this tree satisfies: adding text never lowers the count. A non-positive
// maxTokens yields 0, and a whole string that already fits yields total
// without a search.
func longestFittingPrefixLen(ctx context.Context, counter provider.TokenCounter, total, maxTokens int, pick func(n int) string) (int, error) {
	if maxTokens <= 0 || total == 0 {
		return 0, nil
	}
	whole, err := counter.Count(ctx, pick(total))
	if err != nil {
		return 0, err
	}
	if whole <= maxTokens {
		return total, nil
	}
	lo, hi := 0, total
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		n, cerr := counter.Count(ctx, pick(mid))
		if cerr != nil {
			return 0, cerr
		}
		if n <= maxTokens {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo, nil
}
