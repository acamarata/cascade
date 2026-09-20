package context

import (
	"context"
	"time"
)

// Purpose: the data types the rolling summarizer (summarizer_core.go,
//   summarizer_dispatch.go) uses: the three-member Granularity vocabulary
//   R-14.61 names (turn-window, thread, epoch), each level's token window
//   and summary-size bound, the budget thresholds that SELECT a level, the
//   Clock seam (02 §v1.1 -- no bare time.Now), the persisted SummaryRecord
//   shape, and the two non-fatal events the engine publishes when a
//   regeneration fails or a stale summary is detected.
// Inputs: none -- pure type definitions and code-default tables.
// Outputs: none.
// Constraints: 05-PEWS-PLAN-W4-W6.md Wave 5 Epic U S-46.T2; R-14.61 (the
//   three granularities, named); R-14.62 (no [context.summarizer.*] config
//   keys -- every number here is a code default, never read from config).
//   Event Reason strings carry the failing stage and the failure's taxonomy
//   kind and message (the dependency's own error text, which this package
//   never builds from source content) -- never the summarized content
//   itself and never a summary's Content.
// SPORT: context-engine/summarizer-types (ADD, P1-E21-W5-S46-T2).

// Granularity identifies which of the three rolling-summary scopes a
// SummaryRecord belongs to (R-14.61, T0-named, verbatim): turn-window
// (recent-N turns), thread (per topic-thread), or epoch (cross-thread
// period). The zero value is deliberately not a member, matching
// SlotKind's identical convention in composer_types.go: a forgotten Level
// field reads as a bug, not as turn-window.
type Granularity uint8

const (
	_ Granularity = iota // 0 is deliberately not a valid Granularity

	// GranularityTurnWindow scopes a summary to the recent-N turns of one
	// entity (e.g. one conversation thread's tail). It is the LEAST
	// compressed of the three: it keeps the most detail per unit of source.
	GranularityTurnWindow
	// GranularityThread scopes a summary to one entire topic thread.
	GranularityThread
	// GranularityEpoch scopes a summary to a cross-thread time period. It is
	// the MOST compressed of the three: the widest scope rendered in the
	// least text.
	GranularityEpoch
)

// granularityNames holds the display string for each valid Granularity,
// indexed by value. Index 0 is the invalid zero value's placeholder.
var granularityNames = [...]string{
	"",
	"turn-window",
	"thread",
	"epoch",
}

// String returns the granularity's stable lowercase-hyphenated name, e.g.
// "turn-window" or "epoch". Used in storage keys and event Reason strings.
func (g Granularity) String() string {
	if !g.Valid() {
		return "invalid-granularity"
	}
	return granularityNames[g]
}

// Valid reports whether g is one of the three R-14.61 granularities.
func (g Granularity) Valid() bool {
	return g >= GranularityTurnWindow && g <= GranularityEpoch
}

// windowTokens holds each granularity's code-default source-content window,
// in TOKENS as measured by the injected provider.TokenCounter (R-14.62: no
// [context.summarizer.*] config keys -- these are fixed, not caller- or
// config-tunable). Index matches Granularity's own numeric value; index 0
// is unused (the invalid zero value never reaches a window lookup --
// GetSummary refuses it first).
//
// The three sizes grow 4x with each level's scope: turn-window covers a
// conversation's recent tail, thread covers a whole topic's history, epoch
// covers a cross-thread period.
var windowTokens = [...]int{
	0,     // invalid
	4000,  // turn-window
	16000, // thread
	64000, // epoch
}

// compressionDivisors holds each level's source-to-summary compression
// factor, and is the ONE table the summary-size bounds and the budget
// thresholds are both derived from.
//
// WHY THESE NUMBERS. A turn-window summary may spend at most an eighth of
// the source it was built from: less than that and a recent-turns summary
// stops being a usable stand-in for the turns. Each wider scope covers 4x
// more source (windowTokens above) and must still land in HALF the text of
// the level below it, because a wider scope is asked for precisely when
// there is less room -- so its divisor is 8x the previous one:
// 8 -> 64 -> 512. The resulting bounds are 500, 250 and 125 tokens.
var compressionDivisors = [...]int{
	0,   // invalid
	8,   // turn-window: 4000 / 8   = 500
	64,  // thread:     16000 / 64  = 250
	512, // epoch:      64000 / 512 = 125
}

// Budget thresholds for granularity selection, derived from the summary
// bounds above: a level is affordable exactly when the remaining budget can
// hold that level's whole summary. So the two boundaries are the two
// higher-detail levels' own bounds -- 500 for turn-window and 250 for
// thread -- and anything below 250 gets the most compressed level, whose
// own 125-token bound fits in what little room is left.
//
// They are written here as literals, not as expressions over the tables,
// so a change to either table does not silently move the thresholds;
// TestSummarizerBudgetThresholdsMatchTheirDerivation pins the two sides
// together and fails when they drift apart.
const (
	// budgetForTurnWindow is the remaining-budget floor at or above which
	// the least-compressed level is affordable.
	budgetForTurnWindow = 500
	// budgetForThread is the floor at or above which the thread level is
	// affordable. Below it, only the epoch level's bound fits.
	budgetForThread = 250
)

// DefaultWindowTokens returns g's code-default source-content window size,
// in tokens: the most-recent slice of source content a regeneration
// truncates to before dispatching a model.execute call
// (summarizer_dispatch.go). 0 for an invalid Granularity -- callers that
// skip Valid() truncate to nothing rather than indexing out of range.
func DefaultWindowTokens(g Granularity) int {
	if !g.Valid() {
		return 0
	}
	return windowTokens[g]
}

// MaxSummaryTokens returns the largest summary, in tokens, that g's level
// may produce: its window divided by its compression divisor. A model
// response above this bound is not persisted and not served (see
// summarizer_dispatch.go) -- an over-long "summary" is a failed
// summarization, not a large one. 0 for an invalid Granularity.
func MaxSummaryTokens(g Granularity) int {
	if !g.Valid() {
		return 0
	}
	return windowTokens[g] / compressionDivisors[g]
}

// SelectGranularity picks the level to summarize at for a given remaining
// token budget: the least-compressed level whose whole summary still fits.
// A large remaining budget can afford turn-window detail; a small one needs
// the epoch level's tighter gist. A non-positive budget resolves to the
// most compressed level for the same reason -- there is nothing to spend.
func SelectGranularity(remainingBudget int) Granularity {
	switch {
	case remainingBudget >= budgetForTurnWindow:
		return GranularityTurnWindow
	case remainingBudget >= budgetForThread:
		return GranularityThread
	default:
		return GranularityEpoch
	}
}

// Clock abstracts time.Now so the summarizer never reads the wall clock
// directly (forbidigo, 02-TARGET-STRUCTURE.md §v1.1). Declared locally,
// duck-typed, matching internal/conversation.Clock's identical precedent:
// any concrete Clock in the tree (internal/testkit.RealClock/FrozenClock)
// already satisfies this with zero adapter code.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
}

// SummaryRecord is one persisted rolling summary: the entity and
// granularity it was generated for, its text, the source-content version
// it was generated from (staleness detection compares this, never a
// timestamp), and when it was generated.
type SummaryRecord struct {
	// EntityID names the caller-scoped subject this summary covers (e.g. a
	// conversation thread id). Opaque to this package.
	EntityID string
	// Level is the granularity this record was generated for.
	Level Granularity
	// Content is the summary text itself.
	Content string
	// SourceVersion is the caller-supplied version/hash of the source
	// content this summary was generated from. GetSummary treats a stored
	// record stale exactly when the caller's current SourceVersion differs
	// from this value -- never by elapsed time.
	SourceVersion string
	// GeneratedAt is when this record was produced, per the injected
	// Clock. Reporting only; staleness never reads this field.
	GeneratedAt time.Time
}

// SummarizationFailedEvent reports that a regeneration attempt failed.
// Reason names the stage that failed (model.execute dispatch, the size
// check, or the storage write) together with the failure's taxonomy kind
// and message, so an operator can tell a refused dispatch from a full disk
// without reading the source content -- which never appears here.
type SummarizationFailedEvent struct {
	EntityID string
	Level    Granularity
	Reason   string
}

// StaleSummaryWarningEvent reports that a prior summary was found stale and
// its regeneration failed, so the stored summary no longer describes the
// current source. Distinct from SummarizationFailedEvent: this event
// describes the STATE of the stored summary, not why regeneration failed.
type StaleSummaryWarningEvent struct {
	EntityID string
	Level    Granularity
	Reason   string
}

// SummaryEvent is one published summarizer event. Exactly one field is
// non-nil; the two are separate fields rather than one interface so a
// subscriber switches on presence without a type assertion.
type SummaryEvent struct {
	// Failed is set when a regeneration attempt failed.
	Failed *SummarizationFailedEvent
	// Stale is set when a stale stored summary was detected and could not
	// be regenerated.
	Stale *StaleSummaryWarningEvent
}

// SummaryEventPublisher is the seam the daemon composition root injects so
// the summarizer's two non-fatal events reach the real event bus. The
// summarizer itself neither knows nor cares which bus that is: it holds
// this interface, and a nil publisher is a supported configuration that
// drops every event (the engine's own behaviour never depends on whether
// anyone is listening).
type SummaryEventPublisher interface {
	// Publish delivers one event. Implementations must not block for long
	// and must not return an error: an unpublishable event may never turn a
	// served summary into a failure.
	Publish(ctx context.Context, event SummaryEvent)
}

// SummaryOutcome is GetSummary's return value. GetSummary returns a
// non-nil error only for caller misuse (nil ctx, empty EntityID, an
// invalid Granularity) or a real storage read failure; a regeneration
// failure is reported in this value instead, so the compose path is never
// blocked by one. Summarize, the SummarizerGetter adapter, converts a
// Stale or Failed outcome into an error rather than serving it.
type SummaryOutcome struct {
	// Record is the summary returned: freshly regenerated, or the last
	// valid stored record when regeneration was skipped or failed. A zero
	// Record (empty Content) means no summary was ever available.
	Record SummaryRecord
	// Stale is true when Record is a prior summary returned in place of a
	// regeneration that was attempted and failed.
	Stale bool
	// Failed is non-nil when a regeneration attempt failed this call.
	Failed *SummarizationFailedEvent
	// Warning is non-nil exactly when Stale is true.
	Warning *StaleSummaryWarningEvent
}
