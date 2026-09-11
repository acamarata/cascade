// Purpose: table-driven tests encoding the R-21.218 normative table
//   verbatim for ParseVerdict, Consensus, and LoopStop, plus every listed
//   error-path boundary.
// SPORT: conductor.flow/ADD (P1-E11-W3-S23-T6).

package conductor

import (
	"errors"
	"testing"
)

func TestParseVerdict_NormativeTable(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want Verdict
	}{
		{"approve_exact", "VERDICT: APPROVE", VerdictApprove},
		{"reject_exact", "VERDICT: REJECT", VerdictReject},
		{"needs_changes_exact", "VERDICT: NEEDS_CHANGES", VerdictNeedsChanges},
		{"lowercase", "verdict: approve", VerdictApprove},
		{"mixed_case", "VeRdIcT: rEjEcT", VerdictReject},
		{"leading_whitespace", "  VERDICT: APPROVE", VerdictApprove},
		{"first_of_multiple_wins", "VERDICT: APPROVE\nVERDICT: REJECT", VerdictApprove},
		{"first_of_multiple_wins_reverse", "VERDICT: REJECT\nVERDICT: APPROVE", VerdictReject},
		{"prose_without_prefix_never_matches", "I approve of this change.", VerdictUnknown},
		{"word_in_prose_with_colon_elsewhere", "my verdict on this: it's fine, I approve", VerdictUnknown},
		{"embedded_mid_line_no_match", "not a VERDICT: APPROVE line since text precedes it", VerdictUnknown},
		{"empty_string", "", VerdictUnknown},
		{"trailing_suffix_no_match", "VERDICT: APPROVED", VerdictUnknown},
		{"needs_changes_case", "Verdict: Needs_Changes", VerdictNeedsChanges},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseVerdict(tc.raw)
			if got != tc.want {
				t.Fatalf("ParseVerdict(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseVerdict_NoMatchIsUnknownFailClosed(t *testing.T) {
	got := ParseVerdict("no marker anywhere in this text")
	if got != VerdictUnknown {
		t.Fatalf("got %q, want VerdictUnknown", got)
	}
	// Fail-closed contract: VerdictUnknown must count as REJECT everywhere
	// Consensus consumes it.
	d := Consensus(ConsensusInput{Reviewers: []ReviewerVerdict{
		{Verdict: got, LaneBaseShadowPrice: 1.0},
	}})
	if d.Approved {
		t.Fatalf("VerdictUnknown must not contribute to APPROVE share")
	}
}

// consensusWeightedShareCases is the R-21.218 weighted-share/veto table,
// pulled out of the test function so the function itself stays under the
// funlen limit.
var consensusWeightedShareCases = []struct {
	name      string
	reviewers []ReviewerVerdict
	want      bool
}{
	{
		name: "single_approve",
		reviewers: []ReviewerVerdict{
			{Verdict: VerdictApprove, LaneBaseShadowPrice: 1.0},
		},
		want: true,
	},
	{
		name: "two_thirds_weighted_approve",
		reviewers: []ReviewerVerdict{
			{Verdict: VerdictApprove, LaneBaseShadowPrice: 2.0},
			{Verdict: VerdictReject, LaneBaseShadowPrice: 1.0},
		},
		want: true,
	},
	{
		name: "majority_reject",
		reviewers: []ReviewerVerdict{
			{Verdict: VerdictReject, LaneBaseShadowPrice: 2.0},
			{Verdict: VerdictApprove, LaneBaseShadowPrice: 1.0},
		},
		want: false,
	},
	{
		name: "all_zero_price_equal_weights_majority_approve",
		reviewers: []ReviewerVerdict{
			{Verdict: VerdictApprove, LaneBaseShadowPrice: 0},
			{Verdict: VerdictApprove, LaneBaseShadowPrice: 0},
			{Verdict: VerdictReject, LaneBaseShadowPrice: 0},
		},
		want: true,
	},
	{
		name: "high_price_veto_overrides_majority",
		reviewers: []ReviewerVerdict{
			{Verdict: VerdictApprove, LaneBaseShadowPrice: 1.0},
			{Verdict: VerdictApprove, LaneBaseShadowPrice: 1.0},
			{Verdict: VerdictReject, LaneBaseShadowPrice: 5.0},
		},
		want: false,
	},
	{
		name: "low_price_reject_no_veto_but_still_majority_approve",
		reviewers: []ReviewerVerdict{
			{Verdict: VerdictApprove, LaneBaseShadowPrice: 4.9},
			{Verdict: VerdictReject, LaneBaseShadowPrice: 4.0},
		},
		want: true,
	},
	{
		name: "needs_changes_counts_as_reject",
		reviewers: []ReviewerVerdict{
			{Verdict: VerdictApprove, LaneBaseShadowPrice: 1.0},
			{Verdict: VerdictNeedsChanges, LaneBaseShadowPrice: 1.0},
		},
		want: false, // share exactly 0.5 -> tie -> REJECT
	},
}

func TestConsensus_WeightedShareAndVeto(t *testing.T) {
	for _, tc := range consensusWeightedShareCases {
		t.Run(tc.name, func(t *testing.T) {
			d := Consensus(ConsensusInput{Reviewers: tc.reviewers})
			if d.Approved != tc.want {
				t.Fatalf("Consensus(%+v).Approved = %v, want %v", tc.reviewers, d.Approved, tc.want)
			}
			if d.Err != nil {
				t.Fatalf("unexpected error: %v", d.Err)
			}
		})
	}
}

func TestConsensus_TieAndEmptyQuorumReject(t *testing.T) {
	t.Run("exact_half_tie_rejects", func(t *testing.T) {
		d := Consensus(ConsensusInput{Reviewers: []ReviewerVerdict{
			{Verdict: VerdictApprove, LaneBaseShadowPrice: 1.0},
			{Verdict: VerdictReject, LaneBaseShadowPrice: 1.0},
		}})
		if d.Approved {
			t.Fatalf("exact 0.5 share must reject")
		}
		if d.Err != nil {
			t.Fatalf("tie is not an error path: %v", d.Err)
		}
	})
	t.Run("empty_quorum_rejects_with_typed_error", func(t *testing.T) {
		d := Consensus(ConsensusInput{})
		if d.Approved {
			t.Fatalf("empty reviewer set must reject")
		}
		if !errors.Is(d.Err, ErrInvalidRequest) {
			t.Fatalf("want ErrInvalidRequest, got %v", d.Err)
		}
	})
}

func TestLoopStop_AllThreeConditions(t *testing.T) {
	t.Run("identical_consecutive_rounds", func(t *testing.T) {
		st := LoopState{
			Rounds: [][]Verdict{
				{VerdictApprove, VerdictReject},
				{VerdictReject, VerdictApprove}, // order-independent equal set
			},
			CostCeiling: 100,
		}
		if !LoopStop(st) {
			t.Fatalf("identical consecutive verdict sets must stop")
		}
	})
	t.Run("differing_consecutive_rounds_continue", func(t *testing.T) {
		st := LoopState{
			Rounds: [][]Verdict{
				{VerdictApprove, VerdictReject},
				{VerdictApprove, VerdictApprove},
			},
			CostCeiling: 100,
		}
		if LoopStop(st) {
			t.Fatalf("differing verdict sets must not stop")
		}
	})
}

// TestLoopStop_CapsAndBoundaries covers the attempt-cap and cost-ceiling
// stop conditions; split from TestLoopStop_AllThreeConditions to stay
// under the linter's funlen limit.
func TestLoopStop_CapsAndBoundaries(t *testing.T) {
	t.Run("attempt_cap_boundary", func(t *testing.T) {
		if LoopStop(LoopState{Attempts: 2, CostCeiling: 100}) {
			t.Fatalf("attempts below cap must not stop")
		}
		if !LoopStop(LoopState{Attempts: 3, CostCeiling: 100}) {
			t.Fatalf("attempts at cap must stop")
		}
	})
	t.Run("cost_ceiling_boundary", func(t *testing.T) {
		if LoopStop(LoopState{CostAccrued: 99, CostCeiling: 100}) {
			t.Fatalf("cost below ceiling must not stop")
		}
		if !LoopStop(LoopState{CostAccrued: 100, CostCeiling: 100}) {
			t.Fatalf("cost at ceiling must stop")
		}
	})
	t.Run("single_round_never_stops_on_repetition_rule", func(t *testing.T) {
		st := LoopState{
			Rounds:      [][]Verdict{{VerdictApprove}},
			CostCeiling: 100,
		}
		if LoopStop(st) {
			t.Fatalf("a single round has no prior round to repeat")
		}
	})
	t.Run("zero_ceiling_never_stops_on_cost_alone", func(t *testing.T) {
		if LoopStop(LoopState{CostAccrued: 0, CostCeiling: 0, Rounds: [][]Verdict{{VerdictApprove}}}) {
			t.Fatalf("an unconfigured (zero) ceiling must not trigger the cost-stop condition")
		}
	})
}

func TestSameVerdictSet_CountSensitive(t *testing.T) {
	a := []Verdict{VerdictApprove, VerdictApprove, VerdictReject}
	b := []Verdict{VerdictApprove, VerdictReject}
	if sameVerdictSet(a, b) {
		t.Fatalf("multisets of different composition must not compare equal")
	}
	c := []Verdict{VerdictReject, VerdictApprove, VerdictApprove}
	if !sameVerdictSet(a, c) {
		t.Fatalf("order must not matter")
	}
}
