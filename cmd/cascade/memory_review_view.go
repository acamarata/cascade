// Purpose: how `cascade memory review` renders. The listing view exists to
//
//	let a human DISAGREE with the machine, so it prints the evidence
//	(references, distinct sessions) and the thresholds those counts are
//	being judged against, rather than a verdict the reader would have to
//	take on trust.
//
// Inputs: decoded review.ListResult / review.ActResult values.
// Outputs: fmt.Stringer views for internal/output.Writer.Result; the
//
//	--json payload is each result's own shape, so the two views cannot
//	drift apart.
//
// Constraints: never writes to os.Stdout/os.Stderr (internal/output's
//
//	contract); a candidate's DRAFT TEXT never reaches here at all — the
//	RPC result carries addresses, counts and statuses only — and the
//	unreadable rows' diagnostics are scrubbed of machine paths and
//	secret-shaped values on the way out (memory_view.go's scrubText).
//
// SPORT: cmd.cascade.cmd.memory.review (ADD, P1-E07-W2-S14-T3).
package main

import (
	"bytes"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/memory/review"
)

// reviewListView renders memory.review.list. The embedding is anonymous so
// the --json payload is ListResult's own shape.
type reviewListView struct {
	review.ListResult
}

// String renders the queue.
//
// The header states the instant the queue was read and the thresholds in
// force, because every other line is a claim relative to those two: a row
// is in the pending section BECAUSE its counts are below the threshold,
// and it is visible at all BECAUSE no live defer covers the read instant.
func (v reviewListView) String() string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "review queue as of %s\n", formatInstant(&v.At))
	fmt.Fprintf(&b, "promotion is mechanical at %d reference(s) across %d session(s); "+
		"nothing below that has been promoted\n", v.MinRefCount, v.MinSessions)
	v.writePending(&b)
	v.writePromoted(&b)
	v.writeFooters(&b)
	return strings.TrimRight(b.String(), "\n")
}

// writePending renders the human lane's section.
func (v reviewListView) writePending(b *bytes.Buffer) {
	b.WriteString("\nPENDING - below the threshold, awaiting your decision (not recommendations)\n")
	if len(v.Pending) == 0 {
		b.WriteString("  none\n")
		return
	}
	writeCandidateTable(b, v.Pending, "SNOOZED UNTIL", func(c memory.CandidateSummary) string {
		return formatInstant(c.SnoozeUntil)
	})
}

// writePromoted renders the promotions a revert can still take back.
func (v reviewListView) writePromoted(b *bytes.Buffer) {
	b.WriteString("\nPROMOTED - already written to the store; --revert takes one back\n")
	if len(v.Promoted) == 0 {
		b.WriteString("  none\n")
		return
	}
	writeCandidateTable(b, v.Promoted, "PROMOTED AT", func(c memory.CandidateSummary) string {
		return formatInstant(c.PromotedAt)
	})
}

// writeFooters names everything the two tables do not show: candidates a
// defer is hiding, candidates the mechanical lane is about to take, and
// candidates that could not be read.
//
// None of these may be silently omitted. A queue that renders as empty
// while candidates sit deferred, stuck above the threshold, or unreadable
// on disk is telling a reader there is nothing to look at when there is.
func (v reviewListView) writeFooters(b *bytes.Buffer) {
	if v.Snoozed > 0 {
		fmt.Fprintf(b, "\n%d pending candidate(s) hidden by a defer that has not expired\n", v.Snoozed)
	}
	if len(v.DueForAutoPromotion) > 0 {
		fmt.Fprintf(b, "\n%d candidate(s) have crossed the threshold and belong to the "+
			"mechanical lane, not to review:\n", len(v.DueForAutoPromotion))
		for _, c := range v.DueForAutoPromotion {
			fmt.Fprintf(b, "  %s\n", c.ID)
		}
	}
	for _, u := range v.Unreadable {
		fmt.Fprintf(b, "\nunreadable, left untouched: %s: %s\n", u.ID, scrubText(u.Reason))
	}
}

// writeCandidateTable renders one section's rows, with a section-specific
// final column.
func writeCandidateTable(
	b *bytes.Buffer, rows []memory.CandidateSummary,
	lastHeader string, last func(memory.CandidateSummary) string,
) {
	tw := tabwriter.NewWriter(b, 0, 4, 2, ' ', 0)
	// bytes.Buffer never fails a write, so tabwriter's writes through it
	// never do either; errcheck still wants the result handled.
	_, _ = fmt.Fprintf(tw, "  ADDRESS\tKIND\tREFS\tSESSIONS\t%s\n", lastHeader)
	for _, c := range rows {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%d\t%d\t%s\n",
			c.ID, c.Kind, c.RefCount, c.Sessions, last(c))
	}
	_ = tw.Flush()
}

// formatInstant renders an instant in UTC RFC 3339, or "-" for none. It is
// the one time format this view uses, so two columns of the same table
// cannot disagree about what a timestamp looks like.
func formatInstant(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

// reviewActView renders memory.review.act.
type reviewActView struct {
	review.ActResult
}

// String says what the action did and what the candidate now is.
//
// A skip reports that nothing changed, in those words. An action surface
// that printed the same line whether or not the store changed would leave
// a user unable to tell which of the two just happened, which is the same
// failure `memory forget`'s rehearsal wording exists to prevent.
func (v reviewActView) String() string {
	c := v.Item
	switch v.Action {
	case review.ActionSkip:
		return fmt.Sprintf("skipped %s; nothing changed (still %s, %d reference(s) "+
			"across %d session(s))", c.ID, c.Status, c.RefCount, c.Sessions)
	case review.ActionApprove:
		return fmt.Sprintf("promoted %s ahead of the threshold; it is now a durable "+
			"record (%s). Use --revert on it to take that back.", c.ID, c.Status)
	case review.ActionDefer:
		return fmt.Sprintf("deferred %s until %s; its counts are unchanged and the "+
			"mechanical lane may still promote it", c.ID, formatInstant(c.SnoozeUntil))
	case review.ActionRevert:
		return fmt.Sprintf("reverted %s; it is %s, and the next observation restarts "+
			"its count from one. The record the promotion wrote is NOT deleted; "+
			"use `cascade memory forget %s` for that.", c.ID, c.Status, c.ID)
	default:
		return fmt.Sprintf("%s: %s", v.Action, c.ID)
	}
}
