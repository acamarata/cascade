// Purpose: the human rendering of `cascade sync`'s four verbs.
// Constraints: output.Writer.Result calls fmt.Stringer and nothing else
//   (R-14.253 Finding 2), so a result handed over with no view prints
//   Go's default struct formatting. Each view EMBEDS its result rather
//   than copying it, so --json keeps emitting the same document and a
//   new field cannot be dropped from one surface while staying in the
//   other.
// SPORT: cli.sync views (ADD) — P1-E17-W4-S38-T3.

package main

import (
	"bytes"
	"fmt"
	"text/tabwriter"

	syncpkg "github.com/acamarata/cascade/internal/sync"
)

// syncStatusView renders `sync status`.
type syncStatusView struct{ syncpkg.StatusResult }

// String renders one line per domain, with the ineligible ones kept and
// marked — the whole point of the report is to explain why something is
// not syncing, which omitting it cannot do.
func (v syncStatusView) String() string {
	var buf bytes.Buffer
	_, _ = fmt.Fprintf(&buf, "peer tier: %s\n", v.PeerTier)
	_, _ = fmt.Fprintf(&buf, "open conflicts: %d\n\n", v.OpenConflicts)
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "DOMAIN\tSTRATEGY\tSYNCS\tAT\tWHY NOT")
	for _, d := range v.Domains {
		syncs, why := "yes", ""
		if !d.Eligible {
			syncs, why = "no", d.Reason
		}
		at := "?"
		if d.PositionKnown {
			at = fmt.Sprint(d.Position)
		}
		_, _ = fmt.Fprintf(tw, "%s/%s\t%s\t%s\t%s\t%s\n", d.Domain, d.Subkind, d.Strategy, syncs, at, why)
	}
	_ = tw.Flush()
	return buf.String()
}

// syncRunView renders `sync run`.
type syncRunView struct{ syncpkg.RunResult }

// String reports each domain's own outcome. A run where one domain failed
// and four succeeded is four successes and one failure, not a failure.
func (v syncRunView) String() string {
	if len(v.Domains) == 0 {
		return "no domain this peer's tier permits had anything to sync\n"
	}
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "DOMAIN\tRESULT")
	for _, d := range v.Domains {
		result := "synced"
		if !d.Synced {
			result = "failed: " + d.Error
		}
		_, _ = fmt.Fprintf(tw, "%s/%s\t%s\n", d.Domain, d.Subkind, result)
	}
	_ = tw.Flush()
	return buf.String()
}

// syncConflictsView renders `sync conflicts list`.
type syncConflictsView struct{ syncpkg.ConflictsResult }

// String renders the journal rows. The LOSER's hash is shown because that
// is what an operator is looking for: the thing that went.
func (v syncConflictsView) String() string {
	if len(v.Conflicts) == 0 {
		return "no conflicts journaled\n"
	}
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "RECORD\tDOMAIN\tSTRATEGY\tRESOLUTION\tKEPT\tDISCARDED")
	for _, c := range v.Conflicts {
		_, _ = fmt.Fprintf(tw, "%s\t%s/%s\t%s\t%s\t%s\t%s\n",
			c.RecordID, c.Domain, c.Subkind, c.Strategy, c.Resolution,
			sideLabel(c.Winner), sideLabel(c.Loser))
	}
	_ = tw.Flush()
	return buf.String()
}

// sideLabel renders one side of a conflict compactly.
//
// A git-carried conflict has a ref and no hash, and a refused one has no
// winner at all; both render as "-" rather than as an empty column, so a
// reader can tell a missing value from a mis-aligned table.
func sideLabel(s syncpkg.Side) string {
	switch {
	case s.Ref != "":
		return s.Ref
	case s.Hash != "":
		return fmt.Sprintf("%s@%d %s", s.NodeID, s.Revision, s.Hash)
	default:
		return "-"
	}
}

// syncResolveView renders `sync conflicts resolve`.
type syncResolveView struct{ syncpkg.ResolveResult }

// String says what was settled and whether it needed authorization —
// which is a fact worth printing, not only auditing.
func (v syncResolveView) String() string {
	line := fmt.Sprintf("resolved %s: kept the %s side", v.RecordID, v.Keep)
	if v.Elevated {
		line += " (elevated: the server's copy was discarded)"
	}
	return line + "\n"
}
