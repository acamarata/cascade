package context

import "context"

// Purpose: the data types the context Composer (composer_core.go) accepts
//   and returns: the SlotKind vocabulary, the Slot a caller supplies, the
//   SummarizerGetter dependency-injection seam T2's rolling summarizer
//   implements, and the ComposedResult (with its BudgetTrimEvent and
//   ZeroSlotsEvent reporting) a caller inspects to assert the
//   bounded-context invariant for itself.
// Inputs: none -- pure type definitions.
// Outputs: none.
// Constraints: 05-PEWS-PLAN-W4-W6.md Wave 5 Epic U S-46.T1; no field here
//   ever carries a Label populated from user conversation content -- Label
//   is a caller-chosen short identifier (e.g. "gci", "soul", "chunk-3"),
//   never the Slot's own Content, so BudgetTrimEvent and error messages
//   that report a Label never echo conversation data.
// SPORT: context-engine/composer-types (ADD, per T-1 sport_updates).

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
}

// ComposedSlot is one slot that survived assembly, whole or summarized.
type ComposedSlot struct {
	Kind       SlotKind
	Label      string
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
