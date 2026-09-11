// Purpose: three pure Go decision functions for the Conductor FLOW pattern
//   (verdict parsing, weighted consensus, loop-stop), normatively defined by
//   R-21.218 (21-T0-RULINGS-R21.md §G.1). v1 flow.rs
//   (../cascade-v1/crates/cascade-cli/src/cmd/conductor/flow.rs) is read-only
//   corroborating evidence per ARCHIVE-MAP §SPEC-SALVAGE: its grammar
//   (`[[VERDICT: pass]]`) and its unanimity consensus rule are DIFFERENT from
//   the R-21.218 table implemented here, and no behavior in this file is
//   derived from v1 — see testdata/v1-goldens/flow/README.md.
// Inputs: raw reviewer text (ParseVerdict), a caller-supplied
//   ConsensusInput (Consensus), and a caller-supplied LoopState (LoopStop).
// Outputs: a Verdict, a Decision, and a stop/continue bool respectively.
// Constraints: pure functions only — no I/O, no goroutines, no global
//   state, no bare time.Now (A-T4). The cost ceiling and attempt cap are
//   caller-injected configuration on LoopState, never literals read at call
//   time.
// SPORT: conductor.flow/ADD (P1-E11-W3-S23-T6).

package conductor

import (
	"regexp"
	"strings"
)

// Verdict is a reviewer's judgement of a candidate answer. The zero value
// is VerdictUnknown (no permissive zero value: a forgotten Verdict field
// reads as "no verdict", which fail-closed treats as REJECT).
type Verdict string

const (
	// VerdictUnknown is the zero value: no VERDICT: line matched. Every
	// consumer treats VerdictUnknown as REJECT (R-21.218).
	VerdictUnknown Verdict = ""
	// VerdictApprove is the parsed APPROVE verdict.
	VerdictApprove Verdict = "APPROVE"
	// VerdictReject is the parsed REJECT verdict.
	VerdictReject Verdict = "REJECT"
	// VerdictNeedsChanges is the parsed NEEDS_CHANGES verdict.
	VerdictNeedsChanges Verdict = "NEEDS_CHANGES"
)

// verdictLine matches the FIRST line of the form "VERDICT: <word>",
// case-insensitively, with optional leading whitespace. The capture group
// is anchored to the three recognized words with a trailing word boundary
// so "VERDICT: APPROVED" (an extra suffix) does not match.
var verdictLine = regexp.MustCompile(`(?im)^\s*VERDICT:\s*(APPROVE|REJECT|NEEDS_CHANGES)\b`)

// ParseVerdict scans raw line by line for the first line matching
// `^\s*VERDICT:\s*(APPROVE|REJECT|NEEDS_CHANGES)\b`, case-insensitively.
// The first match wins; later matches on subsequent lines are ignored.
// Prose containing an approval word without the VERDICT: prefix never
// matches, and a raw string with no matching line yields VerdictUnknown,
// which fail-closed policy treats as REJECT (R-21.218).
//
// Example:
//
//	v := ParseVerdict("some notes\nVERDICT: approve\nmore notes")
//	// v == VerdictApprove
func ParseVerdict(raw string) Verdict {
	loc := verdictLine.FindStringSubmatchIndex(raw)
	if loc == nil {
		return VerdictUnknown
	}
	word := strings.ToUpper(raw[loc[2]:loc[3]])
	switch word {
	case "APPROVE":
		return VerdictApprove
	case "REJECT":
		return VerdictReject
	case "NEEDS_CHANGES":
		return VerdictNeedsChanges
	default:
		return VerdictUnknown
	}
}

// ReviewerVerdict is one reviewer's verdict and the base_shadow_price of
// the lane that produced it, as supplied by the caller. Consensus performs
// no lookup of its own.
type ReviewerVerdict struct {
	Verdict             Verdict
	LaneBaseShadowPrice float64
}

// ConsensusInput carries the per-reviewer verdicts and lane prices a single
// Consensus call weighs.
type ConsensusInput struct {
	Reviewers []ReviewerVerdict
}

// Decision is the result of Consensus: the aggregate approve/reject outcome
// plus a non-nil Err only on the zero-quorum path (R-21.218).
type Decision struct {
	Approved bool
	Err      error
}

// highPriceVetoThreshold is the base_shadow_price at or above which a
// single REJECT vetoes consensus regardless of the weighted share
// (R-21.218).
const highPriceVetoThreshold = 5.0

// Consensus implements the R-21.218 weighted-share consensus rule.
//
// Each reviewer's weight is its lane's base_shadow_price normalized to
// [0,1] by the maximum base_shadow_price present in the input (an all-zero
// input yields equal weights of 1, since every price divided by the
// maximum — 0 — would otherwise divide by zero). APPROVE is returned only
// when both hold: the weighted share of APPROVE verdicts is strictly
// greater than 0.5, and no REJECT verdict comes from a reviewer whose lane
// base_shadow_price is >= 5.0. A weighted share of exactly 0.5 is a tie and
// resolves to REJECT. VerdictUnknown and VerdictNeedsChanges both count as
// REJECT for the share computation and the veto rule — only VerdictApprove
// counts toward the APPROVE share. An empty reviewer set returns REJECT
// with the typed ErrInvalidRequest (zero-quorum error path).
//
// Example:
//
//	d := Consensus(ConsensusInput{Reviewers: []ReviewerVerdict{
//		{Verdict: VerdictApprove, LaneBaseShadowPrice: 1.0},
//	}})
//	// d.Approved == true
func Consensus(in ConsensusInput) Decision {
	if len(in.Reviewers) == 0 {
		return Decision{Approved: false, Err: ErrInvalidRequest}
	}

	maxPrice := 0.0
	for _, r := range in.Reviewers {
		if r.LaneBaseShadowPrice > maxPrice {
			maxPrice = r.LaneBaseShadowPrice
		}
	}

	totalWeight := 0.0
	approveWeight := 0.0
	vetoed := false
	for _, r := range in.Reviewers {
		weight := 1.0
		if maxPrice > 0 {
			weight = r.LaneBaseShadowPrice / maxPrice
		}
		totalWeight += weight
		if r.Verdict == VerdictApprove {
			approveWeight += weight
		}
		if r.Verdict == VerdictReject && r.LaneBaseShadowPrice >= highPriceVetoThreshold {
			vetoed = true
		}
	}

	if vetoed {
		return Decision{Approved: false}
	}
	if totalWeight == 0 {
		return Decision{Approved: false}
	}
	share := approveWeight / totalWeight
	return Decision{Approved: share > 0.5}
}

// LoopState is the caller-maintained state a single LoopStop call
// evaluates. Rounds holds every round's per-reviewer verdict set so far,
// oldest first. CostCeiling and the attempt cap (3, R-21.218) are the only
// literals this file reads; the ceiling itself is caller-injected
// configuration, never a literal read at call time.
type LoopState struct {
	Rounds      [][]Verdict
	Attempts    int
	CostAccrued int64
	CostCeiling int64
}

// maxLoopAttempts is the R-21.218 attempt cap.
const maxLoopAttempts = 3

// LoopStop implements the R-21.218 stop rule: stop (return true) when the
// last two consecutive rounds yielded identical verdict sets (set equality
// over the per-reviewer verdicts, order-independent), or Attempts has
// reached the cap of 3, or CostAccrued has reached CostCeiling.
//
// Example:
//
//	stop := LoopStop(LoopState{Attempts: 3, CostCeiling: 100})
//	// stop == true
func LoopStop(st LoopState) bool {
	if st.Attempts >= maxLoopAttempts {
		return true
	}
	if st.CostCeiling > 0 && st.CostAccrued >= st.CostCeiling {
		return true
	}
	n := len(st.Rounds)
	if n >= 2 && sameVerdictSet(st.Rounds[n-1], st.Rounds[n-2]) {
		return true
	}
	return false
}

// sameVerdictSet reports whether a and b contain the same verdicts as
// multisets (order-independent, but count-sensitive: two APPROVEs and one
// REJECT is not the same set as one APPROVE and one REJECT).
func sameVerdictSet(a, b []Verdict) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[Verdict]int, len(a))
	for _, v := range a {
		counts[v]++
	}
	for _, v := range b {
		counts[v]--
	}
	for _, c := range counts {
		if c != 0 {
			return false
		}
	}
	return true
}
