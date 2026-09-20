package context

import (
	"fmt"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the Summarizer's typed-error constructors, plus the failure
//   ATTRIBUTION half: turning a regeneration failure into the events that
//   name which stage failed, and refusing to serve a degraded outcome as
//   content.
// Inputs: an error from one of the engine's dependencies, or nothing.
// Outputs: *cascade.Error values, and SummaryOutcome events.
// Constraints: 12-QUALITY-CONSTITUTION.md Art.1/Art.3 (typed errors only,
//   frozen 14-kind taxonomy, never a bare fmt.Errorf at this boundary).
//   Every message this file BUILDS is made of fixed strings plus EntityID,
//   Level, a token count or a taxonomy kind -- never source content and
//   never a summary's Content, matching composer_errors.go's identical
//   privacy rule in this package. A dependency's own error text is carried
//   verbatim into an event Reason because that text is what distinguishes a
//   refused dispatch from a full disk; the summarizer never places content
//   into an error it hands a dependency, so no content can return that way.
//   NOTE: (*cascade.Error).Is compares Kind ONLY (pkg/cascade/errors.go) --
//   a caller that needs to distinguish two of these constructors from each
//   other must also check the message, never errors.Is alone.
// SPORT: context-engine/summarizer-errors (ADD, P1-E21-W5-S46-T2).

// errSummarizerNilExecutor reports that NewSummarizer was given a nil
// provider.ModelExecutor. A Summarizer with no dispatch door can never
// regenerate anything, so this refuses at construction.
func errSummarizerNilExecutor() error {
	return cascade.New(cascade.KindInvalidInput,
		"context: summarizer: ModelExecutor must not be nil")
}

// errSummarizerNilStore reports that NewSummarizer was given a nil
// provider.Store. A Summarizer with no persistence seam can never
// round-trip a summary (R-14.62), so this refuses at construction.
func errSummarizerNilStore() error {
	return cascade.New(cascade.KindInvalidInput,
		"context: summarizer: Store must not be nil")
}

// errSummarizerNilCounter reports that NewSummarizer was given a nil
// provider.TokenCounter. Every window, every size bound and every
// substitute this engine produces is measured in tokens, so a Summarizer
// with no counter cannot honour any of its own limits.
func errSummarizerNilCounter() error {
	return cascade.New(cascade.KindInvalidInput,
		"context: summarizer: TokenCounter must not be nil")
}

// errSummarizerNilClock reports that NewSummarizer was given a nil Clock.
func errSummarizerNilClock() error {
	return cascade.New(cascade.KindInvalidInput,
		"context: summarizer: Clock must not be nil")
}

// errSummarizerNilContext reports a nil ctx passed to GetSummary.
func errSummarizerNilContext() error {
	return cascade.New(cascade.KindInvalidInput,
		"context: summarizer: ctx must not be nil")
}

// errSummarizerEmptyEntityID reports an empty entityID passed to
// GetSummary -- the storage key's other half (R-14.62's {entity_id,
// granularity_level} keying) can never be blank.
func errSummarizerEmptyEntityID() error {
	return cascade.New(cascade.KindInvalidInput,
		"context: summarizer: entityID must not be empty")
}

// errSummarizerInvalidGranularity reports an out-of-range Granularity
// (this ticket's own "out-of-range granularity level -> error" hard rule).
// Reports the raw numeric value, never a Content field.
func errSummarizerInvalidGranularity(level Granularity) error {
	return cascade.Newf(cascade.KindInvalidInput,
		"context: summarizer: invalid granularity level %d, want one of turn-window/thread/epoch", uint8(level))
}

// errSummarizerOversizeOutput reports a model response longer than the
// level's own summary bound. KindIntegrity: a verification step on the
// response failed, which is what an over-long summary is.
func errSummarizerOversizeOutput(level Granularity, got, bound int) error {
	return cascade.Newf(cascade.KindIntegrity,
		"context: summarizer: model response size %d tokens exceeds the %s bound of %d tokens",
		got, level.String(), bound)
}

// errSummarizerDependency wraps a dependency failure, preserving its Kind
// when it already carries one -- composer_errors.go's errComposerDependency
// precedent in this package.
func errSummarizerDependency(err error, msg string) error {
	if k, ok := cascade.KindOf(err); ok {
		return cascade.Wrap(k, err, msg)
	}
	return cascade.Wrap(cascade.KindInternal, err, msg)
}

// degradeOnFailure builds the SummaryOutcome GetSummary returns when
// regeneration was attempted and failed: last-valid-or-nil plus a
// SummarizationFailedEvent naming the stage that failed, and additionally a
// StaleSummaryWarningEvent when a prior valid record is what is left in the
// store.
func degradeOnFailure(entityID string, level Granularity, existing SummaryRecord, found bool, cause error) SummaryOutcome {
	failed := &SummarizationFailedEvent{EntityID: entityID, Level: level, Reason: failureReason(cause)}
	if !found {
		return SummaryOutcome{Failed: failed}
	}
	warning := &StaleSummaryWarningEvent{
		EntityID: entityID, Level: level,
		Reason: "stored summary describes source version " + existing.SourceVersion +
			" and its regeneration failed: " + failureReason(cause),
	}
	return SummaryOutcome{Record: existing, Stale: true, Failed: failed, Warning: warning}
}

// failureReason renders one regeneration failure's taxonomy kind and
// message. The message is the engine's own stage string ("model.execute
// dispatch failed", "writing summary to storage", "model response size ...")
// with the dependency's text appended by the wrap, so a reader can tell a
// refused dispatch from a lane shortage from a failed store write.
//
// A *cascade.Error already renders its own kind as its leading token
// (pkg/cascade/errors.go), so the kind is only prepended when it is not
// already there -- otherwise every reason read "integrity: integrity: ...".
func failureReason(cause error) string {
	if cause == nil {
		return "regeneration failed for an unreported reason"
	}
	k, ok := cascade.KindOf(cause)
	if !ok {
		return "untyped: " + cause.Error()
	}
	msg := cause.Error()
	if strings.HasPrefix(msg, k.String()+":") {
		return msg
	}
	return fmt.Sprintf("%s: %s", k.String(), msg)
}

// refuseDegradedOutcome converts a degraded GetSummary outcome into the
// error the SummarizerGetter seam owes its caller. A stale or failed summary
// must never be handed to Compose as content: Compose treats an error as "no
// substitute available" and drops the slot whole (composer_core.go's
// resolveOverflow), which is the fail-closed answer.
func refuseDegradedOutcome(outcome SummaryOutcome) error {
	if outcome.Stale {
		return cascade.Newf(cascade.KindUnavailable,
			"context: summarizer: refusing to serve a stale summary: %s", outcome.Warning.Reason)
	}
	if outcome.Failed != nil {
		return cascade.Newf(cascade.KindUnavailable,
			"context: summarizer: no fresh summary available: %s", outcome.Failed.Reason)
	}
	return nil
}
