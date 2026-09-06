package eval

// Purpose: FusionDefaultGate — the single measured boolean R-16.9 ties the
//   shipped retrieval.fusion.enabled default to. It passes only when the
//   semantic/paraphrase RRF-fused recall@10 improves on FTS5-only by at
//   least 10 percent relative AND no known-item query loses an item the
//   FTS5-only leg already found.
// Inputs: the semantic/paraphrase query set's harness results and the
//   known-item query set's harness results (harness.go's RunResult).
// Outputs: a GateVerdict, or a *cascade.Error when either result set
//   cannot honestly be measured (a zero FTS5 denominator, a malformed or
//   incomplete result set).
// Constraints: the measured boolean is the sole allowed default — this
//   function never returns Pass=true on anything short of both
//   conditions holding, and a measurement failure is itself a gate
//   failure, never a permissive default (Art.1).
// SPORT: placeholder: retrieval/evaluation (ADD, P1-E06-W2-S12-T6).

import "fmt"

// minRelativeLift is R-16.9's threshold: the RRF-fused semantic/paraphrase
// recall@10 must be at least 10 percent better, relative, than
// FTS5-only's.
const minRelativeLift = 0.10

// FusionDefaultGate measures R-16.9's fusion default: relative recall@10
// lift on semanticResults, no-known-item-loss on knownItemResults.
func FusionDefaultGate(
	semanticQueries []Query, semanticResults []RunResult,
	knownItemQueries []Query, knownItemResults []RunResult,
) (GateVerdict, error) {
	fts5Recall, err := RecallAt10(semanticQueries, fts5OnlyOf(semanticResults))
	if err != nil {
		return GateVerdict{}, err
	}
	fusedRecall, err := RecallAt10(semanticQueries, fusedOf(semanticResults))
	if err != nil {
		return GateVerdict{}, err
	}
	lift, err := RelativeRecallLift(fts5Recall, fusedRecall)
	if err != nil {
		return GateVerdict{}, err
	}
	lossFree, lossDetail, err := NoKnownItemLoss(
		knownItemQueries, fts5OnlyOf(knownItemResults), fusedOf(knownItemResults))
	if err != nil {
		return GateVerdict{}, err
	}
	verdict := GateVerdict{
		Pass:                lift >= minRelativeLift-recallEpsilon && lossFree,
		RelativeLift:        lift,
		KnownItemLossFree:   lossFree,
		SemanticFTS5Recall:  fts5Recall,
		SemanticFusedRecall: fusedRecall,
	}
	verdict.Detail = gateDetail(verdict, lossDetail)
	return verdict, nil
}

// gateDetail explains a failing verdict; a passing verdict carries no
// detail because there is nothing to explain.
func gateDetail(v GateVerdict, lossDetail string) string {
	if v.Pass {
		return ""
	}
	if !v.KnownItemLossFree {
		return lossDetail
	}
	return fmt.Sprintf(
		"relative recall@10 lift %.4f (fts5-only %.4f, fused %.4f) is below the required %.2f threshold",
		v.RelativeLift, v.SemanticFTS5Recall, v.SemanticFusedRecall, minRelativeLift)
}

// fts5OnlyOf projects a harness run to its FTS5-only rankings.
func fts5OnlyOf(results []RunResult) []RankedResult {
	out := make([]RankedResult, len(results))
	for i, r := range results {
		out[i] = r.FTS5Only
	}
	return out
}

// fusedOf projects a harness run to its RRF-fused rankings.
func fusedOf(results []RunResult) []RankedResult {
	out := make([]RankedResult, len(results))
	for i, r := range results {
		out[i] = r.Fused
	}
	return out
}
