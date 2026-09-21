package context

import "context"

// Purpose: the data types the context Composer (composer_core.go) accepts
//   and returns: the SlotKind vocabulary, the Slot a caller supplies, the
//   SummarizerGetter dependency-injection seam T2's rolling summarizer
//   implements, the ComposedResult (with its BudgetTrimEvent and
//   ZeroSlotsEvent reporting) a caller inspects to assert the
//   bounded-context invariant for itself, and -- added by
//   P1-E21-W5-S46-T3 -- the PipelineStage contract: the stage interface
//   itself, the StageInput a stage transforms in place, and the
//   degrade-event seam the composer publishes through when a stage cannot
//   run.
// Inputs: none -- pure type definitions.
// Outputs: none.
// Constraints: 05-PEWS-PLAN-W4-W6.md Wave 5 Epic U S-46.T1; no field here
//   ever carries a Label populated from user conversation content -- Label
//   is a caller-chosen short identifier (e.g. "gci", "soul", "chunk-3"),
//   never the Slot's own Content, so BudgetTrimEvent and error messages
//   that report a Label never echo conversation data. The same rule binds
//   StageDegradedEvent: every field on it is a fixed, code-chosen string.
// SPORT: context-engine/composer-types (ADD, per T-1 sport_updates;
//   PipelineStage/StageInput/StageEvent ADD, P1-E21-W5-S46-T3).

// SlotKind identifies which of the composer's four content categories a
// Slot belongs to. Unlike provider.SlotKind (three members: tier,
// retrieval, memory), this vocabulary adds history (conversation turns),
// which pkg/provider cannot express without importing internal/conversation
// -- a boundary this package does not cross either, so SlotKind stays
// local to internal/context.
//
// The zero value is intentionally invalid, matching pkg/cascade.Kind's and
// TierRole's convention: a forgotten Kind field reads as a bug.
type SlotKind uint8

const (
	_ SlotKind = iota // 0 is deliberately not a valid SlotKind

	// SlotKindTier is tier-instruction content (GCI..PAI, S-08/S-09).
	SlotKindTier
	// SlotKindMemory is memory/SOUL-store content (G/S-13, G/S-14).
	SlotKindMemory
	// SlotKindRetrieval is retrieval-slot content (fused, cited chunks).
	SlotKindRetrieval
	// SlotKindHistory is conversation-history content (internal/conversation
	// turns/segments).
	SlotKindHistory
)

// slotKindNames holds the display string for each valid SlotKind, indexed
// by SlotKind value. Index 0 is the invalid zero value's placeholder.
var slotKindNames = [...]string{
	"",
	"tier",
	"memory",
	"retrieval",
	"history",
}

// String returns the slot kind's stable lowercase name, e.g. "tier" or
// "history". Used in BudgetTrimEvent and error messages; never echoes
// content.
func (k SlotKind) String() string {
	if !k.Valid() {
		return "invalid-slot-kind"
	}
	return slotKindNames[k]
}

// Valid reports whether k is one of the four defined slot kinds.
func (k SlotKind) Valid() bool {
	return k >= SlotKindTier && k <= SlotKindHistory
}

// Slot is one caller-supplied unit of content Compose may include whole,
// summarize, trim, or drop. Slots are supplied to Compose already in the
// caller's declared priority order (highest-priority first); Compose never
// reorders them.
type Slot struct {
	// Kind names this slot's content category, for reporting only --
	// Compose's assembly loop treats every slot identically regardless of
	// Kind.
	Kind SlotKind
	// Label is a short, caller-chosen identifier for this slot (e.g.
	// "gci", "soul-summary", "chunk-7"). Label is never conversation
	// content and is safe to place in a BudgetTrimEvent, a log line, or an
	// error message.
	Label string
	// Content is the slot's actual text. May carry conversation content
	// and must never be placed in an error message or a BudgetTrimEvent.
	Content string
	// ClassLabel is the category a pre-assembly classify PipelineStage
	// assigned to this slot (pipeline.go, P1-E21-W5-S46-T3). It is empty
	// on every slot a caller supplies -- callers never set it -- and stays
	// empty when no classify stage is attached or when the stage's own
	// response was rejected. Unlike Label it is MODEL-derived, so it is
	// carried onto the result for the caller to read and is never placed
	// in an error message or a BudgetTrimEvent.
	ClassLabel string
}

// ComposedSlot is one slot that survived assembly, whole or summarized.
type ComposedSlot struct {
	Kind  SlotKind
	Label string
	// ClassLabel carries the pre-assembly classify stage's label for this
	// slot through to the caller; see Slot.ClassLabel.
	ClassLabel string
	Content    string
	Tokens     int
	Summarized bool
}

// SummarizerGetter is the dependency-injection seam a rolling summarizer
// (P1-E21-W5-S46-T2) implements: given a slot that does not fit in
// remainingBudget, return a compressed substitute slot that might. A
// Composer with a nil SummarizerGetter simply has no substitution seam --
// an oversized slot goes straight to trim-or-drop.
//
// Summarize must be deterministic for a fixed (slot, remainingBudget) pair,
// matching Compose's own determinism (R-14 truncation requirement):
// otherwise a downstream golden built against one Compose run would not
// reproduce against the next.
type SummarizerGetter interface {
	// Summarize returns a compressed substitute for slot, targeting
	// remainingBudget tokens. An error means no substitute is available;
	// Compose treats that exactly like a nil SummarizerGetter for this one
	// slot -- trim-or-drop, never an aborting failure of the whole
	// Compose call.
	Summarize(ctx context.Context, slot Slot, remainingBudget int) (Slot, error)
}

// BudgetTrimEvent records one slot that did not survive assembly whole: it
// was trimmed to fit remainingTokens, or dropped entirely when no room
// remained. Detail never carries Content -- only Kind, Label and Reason,
// all caller-chosen or code-fixed strings.
type BudgetTrimEvent struct {
	// Kind and Label identify which slot this event is about.
	Kind  SlotKind
	Label string
	// Reason is a fixed, code-chosen explanation (e.g. "budget exceeded,
	// no summarizer configured", "summarizer returned an error", "budget
	// exceeded after summarization"). Never derived from slot content.
	Reason string
	// Dropped is true when the slot was removed entirely (no budget at
	// all remained); false when it was trimmed and a non-empty remainder
	// survived.
	Dropped bool
	// KeptTokens is the token count of whatever survived (0 when Dropped).
	KeptTokens int
}

// ZeroSlotsEvent fires exactly once, when Compose is given zero slots or
// every supplied slot's Content is empty. It carries no fields: there is
// nothing to report beyond the fact itself.
type ZeroSlotsEvent struct{}

// ComposedResult is Compose's return value: every slot that survived, the
// resolved token accounting, and every BudgetTrimEvent/ZeroSlotsEvent that
// fired. TokensUsed never exceeds Budget -- this is the field the bounded-
// context invariant test asserts on every fixture and every generated case.
type ComposedResult struct {
	// Slots holds the surviving content, in the same relative order the
	// caller supplied it.
	Slots []ComposedSlot
	// TokensUsed is the sum of every surviving slot's Tokens. Never
	// exceeds Budget.
	TokensUsed int
	// Budget echoes the budget Compose was called with, so a caller
	// holding only a ComposedResult can still assert the invariant.
	Budget int
	// Trims records every BudgetTrimEvent, in the order slots were
	// processed.
	Trims []BudgetTrimEvent
	// ZeroSlots is non-nil exactly when ZeroSlotsEvent fired.
	ZeroSlots *ZeroSlotsEvent
}

// PipelineStage is one pre- or post-assembly step Compose runs around its
// own assembly pass when a caller attaches one (SetPreStage/SetPostStage,
// composer_config.go). A stage TRANSFORMS its input: it reads the working
// set out of the StageInput the composer owns and writes its result back
// through the same pointer, which is why Execute's own return value carries
// no content. pipeline.go ships the implementations.
//
// A stage never decides whether composition proceeds. Compose degrades to
// plain assembly whenever a stage cannot run (see StageDegradedEvent), so
// attaching one can change the assembled CONTENT but can never turn a
// composition that would have succeeded into a failure.
type PipelineStage interface {
	// TaskClass reports the fixed 06-FORGE-SPEC.md §5.16 class this stage
	// declares on every model.execute call, for callers and tests that
	// want to assert lane affinity without dispatching anything.
	TaskClass() string
	// Execute transforms in. A non-nil error means the stage could not run
	// at all (no lane, a refusal, a dispatch failure); in is then left
	// exactly as the composer handed it over. A nil error with in.Degraded
	// set means the stage ran but REJECTED its own model output and left
	// the working set untouched on purpose.
	Execute(ctx context.Context, in *StageInput) error
}

// StageInput is the working set one PipelineStage reads and rewrites. The
// composer owns the value and passes a pointer; a stage mutates the field
// its own half uses and nothing else.
//
// The two halves are deliberately one type rather than two: the composer
// runs both hooks through the identical Execute signature, so a pre-stage
// and a post-stage are interchangeable at the seam even though a
// pre-assembly stage works on Slots (before there is any assembled text)
// and a post-assembly stage works on Text (after there are no slots left
// to reshape).
type StageInput struct {
	// Slots is the pre-assembly working set: the caller's slots on the way
	// in, and the staged slots -- labelled, or split into more slots than
	// arrived -- on the way out. Empty for a post-assembly stage.
	Slots []Slot
	// Text is the post-assembly working text: the assembled context on the
	// way in, the condensed context on the way out. Empty for a
	// pre-assembly stage.
	Text string
	// Degraded is set by the stage, never by the composer, to a fixed,
	// code-chosen reason when the stage refused its OWN model output (an
	// unparsable response, a condensation that was not shorter). It is the
	// stage's way of reporting "I ran, I declined, nothing changed"
	// without failing the composition; the composer publishes it as a
	// StageDegradedEvent. Never derived from model output or slot content.
	Degraded string
}

// StageEventPublisher is the seam the composition root injects so the
// composer's stage-degrade events reach the real event bus, matching
// SummaryEventPublisher's identical stance for the summarizer: the composer
// neither knows nor cares which bus that is, and a nil publisher is a
// supported configuration that drops every event.
type StageEventPublisher interface {
	// Publish delivers one event. Implementations must not block for long
	// and must not return an error: whether anyone is listening may never
	// change what Compose returns.
	Publish(ctx context.Context, event StageEvent)
}

// StageEvent is one published composer-pipeline event. Exactly one field is
// non-nil, matching SummaryEvent's shape so a subscriber switches on
// presence rather than on a type assertion.
type StageEvent struct {
	// Degraded is set when a pipeline stage did not apply and composition
	// continued without it.
	Degraded *StageDegradedEvent
}

// StageDegradedEvent reports that an attached PipelineStage did not apply
// and Compose returned the un-staged assembly instead. Every field is a
// fixed, code-chosen string: an operator can tell a refused dispatch from a
// rejected response without any slot content or model output appearing
// here.
type StageDegradedEvent struct {
	// Stage is "pre-assembly" or "post-assembly".
	Stage string
	// TaskClass is the §5.16 class the stage declared.
	TaskClass string
	// Reason is one of the fixed reasons pipeline.go and
	// pipeline_compose.go declare. Never derived from content.
	Reason string
	// Kind is the taxonomy kind of the underlying failure (e.g.
	// "unavailable", "permission"), or empty when the stage ran and only
	// rejected its own output.
	Kind string
}
