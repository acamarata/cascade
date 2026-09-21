// Purpose: merge-on-green's WIRE half (P1-E25-W5-S51-T3): the JSON shape the
// cascade-github.prs.merge tool call carries in each direction, the dispatch
// itself, and the audit row every decision and outcome is recorded as. Split
// out of waitmerge_merge.go under Art.10.3's 300-line cap, along the seam
// that matters: waitmerge_merge.go decides, this file speaks.
//
// Inputs: MergeOptions plus, for the dispatch, MergeDeps' injected
// MergeCaller and audit.Writer.
//
// Outputs: a decoded MergeResult, or a typed refusal
// (KindUnavailable/KindIntegrity/KindConflict); and one sealed audit row per
// call.
//
// Constraints: internal/ci may not import plugins/** (Art.10.2), so the two
// wire structs below MIRROR plugins/github/tools.Args/MergeResult's json tags
// rather than importing them -- see waitmerge_merge.go's header for the full
// reasoning and for why the method name is never model.execute.
//
// SPORT: internal.ci.MergeOnGreen/ADDED (P1-E25-W5-S51-T3).

package ci

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// mergeWireParams mirrors plugins/github/tools.Args's json tags this call
// actually needs (see this file's header doc comment).
type mergeWireParams struct {
	Owner       string `json:"owner"`
	Repo        string `json:"repo"`
	Number      int    `json:"number"`
	MergeMethod string `json:"merge_method"`
}

// mergeWireResult mirrors plugins/github/tools.MergeResult's json tags.
type mergeWireResult struct {
	SHA     string `json:"sha"`
	Merged  bool   `json:"merged"`
	Message string `json:"message"`
}

// callMerge dispatches the real plugin-host call and decodes the result.
func (d MergeDeps) callMerge(ctx context.Context, opts MergeOptions) (MergeResult, error) {
	params, err := json.Marshal(mergeWireParams{Owner: opts.Owner, Repo: opts.Repo, Number: opts.PR, MergeMethod: opts.MergeMethod})
	if err != nil {
		return MergeResult{}, cascade.Wrap(cascade.KindInternal, err, "ci: encoding the merge-on-green tool call")
	}
	raw, err := d.Caller.Call(ctx, mergeToolMethod, params)
	if err != nil {
		return MergeResult{}, cascade.Wrap(cascade.KindUnavailable, err, "ci: cascade-github.prs.merge refused")
	}
	var wire mergeWireResult
	if err := json.Unmarshal(raw, &wire); err != nil {
		return MergeResult{}, cascade.Wrap(cascade.KindIntegrity, err, "ci: decoding the merge-on-green response")
	}
	if !wire.Merged {
		return MergeResult{}, cascade.Newf(cascade.KindConflict, "ci: GitHub did not merge PR #%d: %s", opts.PR, wire.Message)
	}
	return MergeResult{Merged: true, SHA: wire.SHA}, nil
}

// record appends one audit row. Its own write failure is never surfaced as
// MergeOnGreen's error (mirrors runner.go's appendJournal: the decision
// already happened, and losing its audit trail must never retroactively
// change what was decided) -- but no decision is ever MADE here, only
// recorded, so this can never mask a refusal.
func (d MergeDeps) record(ctx context.Context, opts MergeOptions, kind audit.Kind,
	outcome policy.EvalOutcome, verdict policy.Verdict, what string,
) {
	params, _ := json.Marshal(mergeWireParams{Owner: opts.Owner, Repo: opts.Repo, Number: opts.PR, MergeMethod: opts.MergeMethod})
	_, _ = d.Audit.Append(ctx, audit.Event{
		Kind:       kind,
		Actor:      opts.Subject.String(),
		Action:     MergeOnGreenCapability,
		ParamsHash: audit.HashParams(params),
		RiskLevel:  outcome.Level.String(),
		Verdict:    verdict.String(),
		Outcome:    what,
	})
}
