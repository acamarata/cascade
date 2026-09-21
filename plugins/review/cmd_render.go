// Purpose (this file): `review` command output rendering (P1-E25-W5-S52-T5),
//
//	split out of cmd.go purely to stay under Art.10.3's 300-line/file cap
//	— same package, no behavior split of its own.
//
// Inputs: a provider.ReviewResponse (cmd.go's runReview) and the --json
//
//	flag's resolved bool.
//
// Outputs: --json writes exactly one reviewEnvelope to cmd.OutOrStdout();
//
//	TTY/human mode writes a ranked finding list there instead. Both write
//	only to stdout — never stderr, and never a bare os.Stdout/fmt.Print
//	(this file is outside cmd/**, so the repo-wide forbidigo scoping does
//	not reach it, but the discipline is kept anyway: it is what makes
//	cmd_test.go's cmd.SetOut(&buf) capture actually work).
//
// Constraints: plugins/review may import pkg/** only, never internal/**
//
//	(Art.10.2) — so this file cannot reuse internal/output.Envelope for
//	--json. reviewEnvelope is a local, minimal, versioned envelope
//	carrying exactly the shape the ticket's own OUTPUT section names
//	({"version":1,"findings":[...]}), not a divergent ad hoc shape. FIX
//	(T0 decision D5, 2026-09-21): the prior draft added an "approved"
//	top-level field neither the ticket's literal OUTPUT text nor
//	07-CLI-COMMAND-TREE §review names; dropped. The human-text renderer
//	below still reports approved/not — that surface was never in
//	question, only the --json wire shape.
//
// SPORT: plugins/review:cmd_render (ADD) — P1-E25-W5-S52-T5.

package review

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// reviewEnvelope is the --json output contract's local, versioned shape
// (see this file's header Constraints note). Findings is always a non-nil,
// possibly-empty slice (never null) so a machine consumer sees a uniform
// shape, matching cmd/cascade/run_exec.go's toRunResultView "Legs...never
// omitted or null" precedent.
type reviewEnvelope struct {
	Version  int                 `json:"version"`
	Findings []reviewFindingView `json:"findings"`
}

// reviewFindingView is one wire finding.
type reviewFindingView struct {
	Severity string `json:"severity"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Message  string `json:"message"`
}

// reviewEnvelopeVersion is this local envelope's wire version (matching
// internal/output.EnvelopeVersion's own starting value; independently
// versioned, since this shape is not that package's Envelope).
const reviewEnvelopeVersion = 1

// toReviewFindingViews projects the ABI's []provider.ReviewFinding onto the
// wire shape, sorted for deterministic output (Art.11 — findings arrive in
// "no particular order" per pkg/provider.ReviewResponse's own doc comment).
func toReviewFindingViews(findings []provider.ReviewFinding) []reviewFindingView {
	out := make([]reviewFindingView, 0, len(findings))
	for _, f := range findings {
		out = append(out, reviewFindingView{Severity: string(f.Severity), File: f.File, Line: f.Line, Message: f.Message})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Message < out[j].Message
	})
	return out
}

// renderReviewResponse writes resp per the D/S-06.T5 stdout=data contract:
// --json emits exactly one reviewEnvelope; TTY/human mode prints a ranked
// finding list.
func renderReviewResponse(cmd *cobra.Command, jsonMode bool, resp provider.ReviewResponse) error {
	views := toReviewFindingViews(resp.Findings)
	if jsonMode {
		return writeReviewJSON(cmd, reviewEnvelope{Version: reviewEnvelopeVersion, Findings: views})
	}
	return writeReviewText(cmd, resp.Approved, views)
}

// writeReviewText renders the human-readable ranked finding list.
func writeReviewText(cmd *cobra.Command, approved bool, views []reviewFindingView) error {
	out := cmd.OutOrStdout()
	if len(views) == 0 {
		_, err := fmt.Fprintf(out, "cascade review: no findings (approved=%t)\n", approved)
		return err
	}
	for i, v := range views {
		loc := v.File
		if v.Line > 0 {
			loc = fmt.Sprintf("%s:%d", v.File, v.Line)
		}
		if loc == "" {
			loc = "-"
		}
		if _, err := fmt.Fprintf(out, "%d. [%s] %s: %s\n", i+1, strings.ToUpper(v.Severity), loc, v.Message); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(out, "\napproved=%t findings=%d\n", approved, len(views))
	return err
}

// writeReviewJSON marshals and writes env as indented JSON, matching
// internal/output.Envelope.MarshalLine's own indent/newline convention so
// --json output looks consistent across the binary even though this
// envelope type is a different, local Go type.
func writeReviewJSON(cmd *cobra.Command, env reviewEnvelope) error {
	line, err := marshalReviewEnvelope(env)
	if err != nil {
		return err
	}
	_, werr := cmd.OutOrStdout().Write(line)
	return werr
}

// marshalReviewEnvelope is split out of writeReviewJSON so cmd_test.go can
// assert the exact wire bytes without constructing a *cobra.Command. A
// marshal failure is wrapped KindInternal, matching
// internal/output.Envelope.MarshalLine's own fallback: reviewEnvelope's
// field types are all this package's own, so the only realistic failure is
// a future field type encoding/json cannot handle.
func marshalReviewEnvelope(env reviewEnvelope) ([]byte, error) {
	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "cascade review: marshal json envelope")
	}
	return append(b, '\n'), nil
}
