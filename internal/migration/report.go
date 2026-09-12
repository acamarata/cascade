package migration

import (
	"bytes"
	"fmt"
	"strings"
	"text/tabwriter"
)

// Purpose: Report's human-readable rendering (internal/output.Writer.Result
//
//	uses this String() in human mode; --json marshals Report's own json
//	tags directly and never calls this method — D/S-06.T5's contract).
//
// Constraints: no os.Stdout/os.Stderr/bare fmt.Print — this returns a
//
//	string; the caller (cmd/cascade) is the only place that ever writes it,
//	through internal/output.Writer.
//
// SPORT: internal/migration [ADD] (P1-E26-W10-S53-T3 sport_updates).

// String renders the drift table, each stale file's full diff body, and
// (apply mode only) the list of files actually written.
func (r Report) String() string {
	if len(r.Projects) == 0 {
		return "0 projects considered"
	}
	out := driftTable(r)
	if body := diffBodies(r); body != "" {
		out += "\n\n" + body
	}
	if applied := appliedLines(r); applied != "" {
		out += "\n\n" + applied
	}
	return out
}

// driftTable renders one row per (project, harness file) pair, or a single
// "(fresh)" row for a project with no drift.
func driftTable(r Report) string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "PROJECT\tHARNESS_FILE\tADDED\tREMOVED\n")
	for _, pr := range r.Projects {
		writeProjectRow(tw, pr)
	}
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// writeProjectRow writes pr's row(s): an error row, a "(fresh)" row, or
// one row per drift entry.
func writeProjectRow(tw *tabwriter.Writer, pr ProjectReport) {
	switch {
	case pr.Error != "":
		_, _ = fmt.Fprintf(tw, "%s\t-\t-\terror: %s\n", pr.ProjectPath, pr.Error)
	case len(pr.Drift) == 0:
		_, _ = fmt.Fprintf(tw, "%s\t(fresh)\t0\t0\n", pr.ProjectPath)
	default:
		for _, d := range pr.Drift {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\n", pr.ProjectPath, d.HarnessFile, d.Added, d.Removed)
		}
	}
}

// diffBodies concatenates every drift entry's full diff body, headed by
// its project and file path.
func diffBodies(r Report) string {
	var b strings.Builder
	for _, pr := range r.Projects {
		for _, d := range pr.Drift {
			fmt.Fprintf(&b, "--- %s : %s ---\n%s\n", pr.ProjectPath, d.HarnessFile, strings.TrimRight(d.DiffBody, "\n"))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// appliedLines renders one line per file Run actually wrote.
func appliedLines(r Report) string {
	if len(r.Applied) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "applied:")
	for _, a := range r.Applied {
		fmt.Fprintf(&b, "\n  %s: %s (%s)", a.ProjectPath, a.Path, a.Action)
	}
	return b.String()
}
