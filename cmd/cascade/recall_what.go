// Purpose: `cascade recall what <query>` (07-CLI-COMMAND-TREE §recall;
// rpc recall.what, R-14.65/66): the fused, cross-domain query surface
// spanning turns, threads, memories, and files. Split out of recall.go,
// which mounts this command but does not implement it (that file's own
// header explains why: a second fused surface belongs beside its own
// tests). CLI half only — the cobra command, the wire params, and the
// rendering; fusion, scope resolution and egress redaction all live in
// internal/retrieval (recallwhat*.go).
//
// Inputs: cobra args (the positional query only — 07 §recall ratifies no
// flag beyond it; a session-scope guess (cwd) sourced from recallDeps.Getwd,
// never a caller-supplied --scope: scope is resolved server-side
// (internal/retrieval/recallwhat_scope.go) and the CLI asserts nothing
// about it. Outputs: internal/output only, via the D/S-06.T5 conventions
// recall.go's own view types already follow.
//
// Constraints: no platform-specific imports (Art.5); a withheld or
// truncated result is reported only as a count, per
// retrieval.RecallWhatResponse's own contract — never a path, corpus or
// id of an excluded row.
//
// SPORT: cmd.cascade.cmd.recall.what (ADD, P1-E22-W5-S47-T1).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/retrieval"
)

// newRecallWhatCmd builds `cascade recall what`.
func newRecallWhatCmd(deps recallDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "what <query>",
		Short: "Fuse memories and files into one cited answer (conversation leg pending)",
		Long: "Run <query> across the domains this build can scope — memories and\n" +
			"files — and print one fused, ranked, cited answer. The conversation\n" +
			"leg is excluded until threads carry a scope reference; it is reported\n" +
			"as an unavailable domain, never silently searched.\n\n" +
			"The session scope is resolved by the daemon from this process's\n" +
			"working directory (E/S-08.T4); there is no --scope flag here, unlike\n" +
			"the bare `cascade recall` command — 07-CLI-COMMAND-TREE §recall\n" +
			"ratifies no flag beyond the positional query for this subcommand.\n\n" +
			"`cascade what <query>` is a hidden top-level alias for this exact\n" +
			"command (07-CLI-COMMAND-TREE §note-2, owner ergonomics): same query,\n" +
			"same output, same exit codes — it does not appear in `cascade --help`\n" +
			"or shell completions.",
		Example: "  cascade recall what \"why did the retry policy change\"\n" +
			"  cascade recall what \"retry policy\" --json\n" +
			"  cascade what \"retry policy\"  # hidden alias, identical output",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			getwd := deps.Getwd
			if getwd == nil {
				getwd = os.Getwd
			}
			cwd, _ := getwd() // best-effort: an unreadable cwd resolves to
			// scope.ScopeKindGeneral server-side (R-16.3), never a refusal here.
			params := retrieval.WhatParams{Query: args[0], Cwd: cwd}
			var result retrieval.WhatResult
			if err := recallCall(cmd, deps, retrieval.MethodWhat, params, &result); err != nil {
				return err
			}
			return recallWriter(cmd).Result(recallWhatView{result: result})
		},
	}
}

// recallWhatView renders recall.what's result, mirroring recallView.
type recallWhatView struct{ result retrieval.WhatResult }

// MarshalJSON emits the RPC result verbatim: --json is the wire shape.
func (v recallWhatView) MarshalJSON() ([]byte, error) { return json.Marshal(v.result) }

// String renders the ranked results as a table (or "no results" in full
// words), the withheld count, the truncated count (fix item 4 — a k-cap
// overflow is never described with the same words as an authorization/
// privacy withholding), and any per-domain unavailability.
func (v recallWhatView) String() string {
	var buf bytes.Buffer
	if len(v.result.Results) == 0 {
		buf.WriteString("no results")
	} else {
		writeRecallWhatTable(&buf, v.result.Results)
	}
	if v.result.Withheld > 0 {
		_, _ = fmt.Fprintf(&buf, "\n%d result(s) withheld: excluded by scope or privacy policy", v.result.Withheld)
	}
	if v.result.Truncated > 0 {
		_, _ = fmt.Fprintf(&buf, "\n%d further result(s) not shown (result cap)", v.result.Truncated)
	}
	if n := len(v.result.Errors); n > 0 {
		_, _ = fmt.Fprintf(&buf, "\n%d domain(s) unavailable (see --json for detail)", n)
	}
	return strings.TrimRight(buf.String(), "\n")
}

// writeRecallWhatTable renders the fused rows, one per domain-tagged row.
func writeRecallWhatTable(buf *bytes.Buffer, results []retrieval.RecallWhatResult) {
	tw := tabwriter.NewWriter(buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "RANK\tDOMAIN\tSCORE\tTRUST\tSOURCE\n")
	for _, r := range results {
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%.3f\t%s\t%s\n",
			r.Rank, r.Domain, r.Score, r.Trust, whatSource(r))
	}
	_ = tw.Flush()
}

// whatSource mirrors recall.go's recallSource for RecallWhatResult: path
// if present else id, control-stripped, width-capped.
func whatSource(r retrieval.RecallWhatResult) string {
	text := r.Path
	if strings.TrimSpace(text) == "" {
		text = r.ID
	}
	text = stripControl(text)
	if len([]rune(text)) > recallPathWidth {
		return "..." + string([]rune(text)[len([]rune(text))-recallPathWidth:])
	}
	return text
}
