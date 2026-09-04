package context

import (
	"strings"
	"unicode/utf8"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the instruction merge model — combines the []TierRecord that
//   discover.go resolves into one MergedContext under the higher-tier-wins
//   precedence rule (R-14.15) and the level-2-heading section grammar
//   (R-14.16).
// Inputs: a []TierRecord ordered by strictly increasing Ordinal, exactly as
//   Discover returns it.
// Outputs: a MergedContext (resolved sections plus per-heading provenance),
//   or a typed cascade.Error. Never a partial merge: a record this engine
//   cannot parse fails the whole call rather than being skipped, because a
//   tier that silently vanishes is an instruction the user believes is in
//   force and is not.
// Constraints: 04-PEWS-PLAN-W1-W3.md Wave 2 Epic E S-08 T2; R-14.15
//   (precedence), R-14.16 (section grammar). Pure and deterministic: no
//   clock, no filesystem, no network, no map iteration on the output path.
// SPORT: context-engine/merge-model (ADD, per T-2 sport_updates).

// Section grammar constants. headingPrefix is the level-2 ATX marker that
// opens a section; fenceBacktick and fenceTilde open and close a code fence,
// inside which a heading-looking line is content, not a boundary.
const (
	headingPrefix = "## "
	fenceBacktick = "```"
	fenceTilde    = "~~~"
)

// MergedSection is one contiguous block of instruction text that survived
// the merge, together with the tier it came from.
//
// A section's identity is its Heading — the text after the "## " marker,
// whitespace-trimmed, compared EXACTLY (case- and punctuation-sensitive), so
// "## Master List" and "## Master Lists" are two different sections and both
// survive. Content is the block's raw text INCLUDING its heading line, with
// trailing newlines trimmed so the same input always renders the same bytes.
//
// The Heading of the block that precedes a file's first "## " marker is ""
// — a preamble. Preambles carry each tier file's title and front matter, so
// they are deliberately NOT conflict-keyed: every non-absent tier's preamble
// survives, at its own tier's position. Only heading-keyed sections compete.
type MergedSection struct {
	// Heading is the section's exact heading text, or "" for a preamble.
	Heading string
	// Content is the block's raw text, heading line included, with
	// trailing newlines trimmed.
	Content string
	// Role is the tier that contributed this block.
	Role TierRole
	// Ordinal is that tier's precedence ordinal, copied from its
	// TierRecord so a section is self-describing without a second lookup.
	Ordinal int
}

// MergedContext is the resolved instruction cascade for one working
// directory: every section that survived the merge, in emission order, plus
// a per-heading record of which tier won it.
//
// Emission order is most-general-first — every section a tier contributes is
// emitted at that tier's position, tiers in ascending ordinal (GCI first,
// PAI last) — so a reader meets the general rules before the specific ones.
// Sections is the ONLY ordered output; Provenance is a lookup table and must
// never be iterated to produce output, because Go map iteration order is
// randomized and doing so would make the merge non-deterministic.
type MergedContext struct {
	// Sections holds the surviving blocks in emission order.
	Sections []MergedSection
	// Provenance maps each surviving section's Heading to the TierRole
	// that won it. Preambles (Heading "") are not keyed here — they do not
	// compete, and several tiers contribute one; each preamble's own
	// MergedSection.Role carries its origin instead. Always non-nil, even
	// for an empty merge.
	Provenance map[string]TierRole
}

// MergeTiers merges tiers into a single MergedContext under the
// higher-tier-wins rule.
//
// # The rule (R-14.15), stated once
//
// The HIGHER tier wins: the LOWEST ordinal (furthest from the working
// directory, highest authority — GCI is ordinal 0) wins a same-heading
// conflict. Higher ordinals (closer to the working directory — PAI is
// highest) ADD sections that no higher tier defined, but never override a
// higher tier's content. Within one record, a heading repeated later is a
// duplicate of the earlier one and the earlier (lower) position wins; the
// same first-claim pass resolves that, so it needs no separate stage.
//
// A tier that DEFINES a heading with an empty body still wins it, and
// suppresses every lower tier's version. That is deliberate and is not the
// same as not mentioning the heading at all: writing an empty section is how
// a higher tier says "this section is intentionally blank", whereas omitting
// it is how it says "whoever knows better may fill this in".
//
// # Failing closed
//
// tiers must be ordered by strictly increasing Ordinal (as Discover
// guarantees) and must not repeat a role. A record whose content cannot be
// read as text, or whose Absent flag contradicts its content, fails the
// whole call. MergeTiers never silently drops a record it cannot handle:
// only TierRecord.Absent — an honest "this tier has no file" from discovery
// — is skipped, and that skip is transparent.
//
// Empty input, and input in which every tier is absent, both merge to an
// empty MergedContext with no error: having no instructions is a legitimate
// outcome, not a failure.
func MergeTiers(tiers []TierRecord) (MergedContext, error) {
	if err := validateTiers(tiers); err != nil {
		return MergedContext{Provenance: map[string]TierRole{}}, err
	}

	merged := MergedContext{Provenance: make(map[string]TierRole, len(tiers)*8)}
	claimed := make(map[string]struct{}, len(tiers)*8)

	for _, rec := range tiers {
		if rec.Absent {
			continue
		}
		for _, blk := range splitSections(rec.Content) {
			if blk.heading == "" {
				merged.Sections = append(merged.Sections, MergedSection{
					Content: blk.content, Role: rec.Role, Ordinal: rec.Ordinal,
				})
				continue
			}
			if _, taken := claimed[blk.heading]; taken {
				continue
			}
			claimed[blk.heading] = struct{}{}
			merged.Provenance[blk.heading] = rec.Role
			merged.Sections = append(merged.Sections, MergedSection{
				Heading: blk.heading, Content: blk.content,
				Role: rec.Role, Ordinal: rec.Ordinal,
			})
		}
	}
	return merged, nil
}

// validateTiers enforces MergeTiers' input contract: valid, non-repeating
// roles in strictly increasing ordinal order, each carrying content this
// engine can actually parse. Every violation is a typed cascade.Error of
// KindInvalidInput — the caller supplied a slice the merge cannot honour,
// and honouring part of it would be the silent-drop failure this whole
// function exists to prevent.
func validateTiers(tiers []TierRecord) error {
	seen := make(map[TierRole]struct{}, len(tiers))
	prev := -1
	for i, rec := range tiers {
		if !rec.Role.Valid() {
			return cascade.Newf(cascade.KindInvalidInput,
				"context: merge: tier at index %d has an invalid role (%d)", i, uint8(rec.Role))
		}
		if _, dup := seen[rec.Role]; dup {
			return cascade.Newf(cascade.KindInvalidInput,
				"context: merge: tier %s appears more than once (index %d)", rec.Role, i)
		}
		seen[rec.Role] = struct{}{}
		if rec.Ordinal <= prev {
			return cascade.Newf(cascade.KindInvalidInput,
				"context: merge: tiers must arrive in strictly increasing ordinal order; %s at index %d has ordinal %d, after %d",
				rec.Role, i, rec.Ordinal, prev)
		}
		prev = rec.Ordinal
		if err := validateContent(rec); err != nil {
			return err
		}
	}
	return nil
}

// validateContent rejects a record whose content cannot be treated as an
// instruction file. Both checks fail the merge rather than dropping the one
// record, per MergeTiers' fail-closed contract.
func validateContent(rec TierRecord) error {
	if rec.Absent {
		if rec.Content != "" {
			return cascade.Newf(cascade.KindInvalidInput,
				"context: merge: tier %s is marked absent but carries %d bytes of content", rec.Role, len(rec.Content))
		}
		return nil
	}
	if !utf8.ValidString(rec.Content) {
		return cascade.Newf(cascade.KindInvalidInput,
			"context: merge: tier %s (%s) is not valid UTF-8 and cannot be parsed as an instruction file", rec.Role, rec.Path)
	}
	if strings.ContainsRune(rec.Content, 0) {
		return cascade.Newf(cascade.KindInvalidInput,
			"context: merge: tier %s (%s) contains a NUL byte and cannot be parsed as an instruction file", rec.Role, rec.Path)
	}
	return nil
}

// sectionBlock is one split-out block of a single record's content, before
// any cross-tier precedence has been applied.
type sectionBlock struct {
	heading string
	content string
}

// splitSections splits one record's content into blocks at level-2 heading
// boundaries. A leading block with no heading is returned with heading ""
// (a preamble) unless it is entirely whitespace, in which case it is
// dropped: an empty preamble is nothing, not a section.
//
// Heading detection deliberately ignores lines inside a fenced code block.
// Instruction files routinely quote markdown at themselves, and splitting on
// a "## " that is part of a quoted example would silently cut a section in
// half and hand its tail a heading its author never wrote.
func splitSections(content string) []sectionBlock {
	if strings.TrimSpace(content) == "" {
		return nil
	}

	var blocks []sectionBlock
	var buf []string
	cur := sectionBlock{}
	inFence := false

	flush := func() {
		if len(buf) == 0 {
			return
		}
		body := strings.TrimRight(strings.Join(buf, "\n"), "\n")
		buf = nil
		if cur.heading == "" && strings.TrimSpace(body) == "" {
			return
		}
		cur.content = body
		blocks = append(blocks, cur)
	}

	for _, line := range strings.Split(content, "\n") {
		if isFenceDelimiter(line) {
			inFence = !inFence
		} else if !inFence && isSectionHeading(line) {
			flush()
			cur = sectionBlock{heading: headingKey(line)}
		}
		buf = append(buf, line)
	}
	flush()
	return blocks
}

// isFenceDelimiter reports whether line opens or closes a code fence. Only
// column-0 fences count: an indented fence inside a list must not be able to
// swallow a column-0 heading that follows it.
func isFenceDelimiter(line string) bool {
	return strings.HasPrefix(line, fenceBacktick) || strings.HasPrefix(line, fenceTilde)
}

// isSectionHeading reports whether line opens a level-2 section. A "## "
// with nothing after it is content, not a heading: an empty heading would
// collide with the preamble's "" key, and a section nobody can name is not
// a section anyone can override.
func isSectionHeading(line string) bool {
	return strings.HasPrefix(line, headingPrefix) && headingKey(line) != ""
}

// headingKey returns line's heading text with surrounding whitespace
// trimmed. It is only meaningful for a line isSectionHeading accepts.
func headingKey(line string) string {
	return strings.TrimSpace(strings.TrimPrefix(line, headingPrefix))
}
