// Purpose: the human-readable explain view of a lane ranking: why each
// candidate is or is not eligible and how its utility adds up.
//
// Inputs: a Ranked candidate or a whole Ranking (rank_types.go).
//
// Outputs: deterministic text; one line per verdict and per term.
//
// Constraints: pure formatting, no ranking logic, no clock or I/O; no line
// ends in a space.

package economics

import (
	"fmt"
	"strings"
)

// ExplainCandidate prints the lane id, eligible yes/no, one line per Verdict
// ("<predicate>: pass|fail|n/a <reason>"), one line per Term
// ("<name> w=<weight> v=<value> c=<contribution>"), then correlation and
// utility.
func ExplainCandidate(r Ranked) string {
	var b strings.Builder
	eligible := "no"
	if r.Eligible {
		eligible = "yes"
	}
	fmt.Fprintf(&b, "lane %s eligible=%s\n", r.Candidate.LaneID, eligible)
	for _, v := range r.Verdicts {
		b.WriteString(verdictLine(v))
		b.WriteByte('\n')
	}
	for _, t := range r.Terms {
		fmt.Fprintf(&b, "%s w=%g v=%g c=%g\n", t.Name, t.Weight, t.Value, t.Contribution)
	}
	fmt.Fprintf(&b, "correlation=%g utility=%g", r.Correlation, r.Utility)
	return b.String()
}

// verdictLine renders one verdict without a trailing space.
func verdictLine(v Verdict) string {
	state := "fail"
	switch {
	case !v.Applicable:
		state = "n/a"
	case v.Pass:
		state = "pass"
	}
	line := v.Predicate + ": " + state
	if reason := strings.TrimSpace(v.Reason); reason != "" {
		line += " " + reason
	}
	return line
}

// ExplainRanking prints a header with the candidate count and RoleFallback,
// then ExplainCandidate for each Order entry in order, separated by one
// blank line. An empty Order prints the header only.
func ExplainRanking(rk Ranking) string {
	var b strings.Builder
	fmt.Fprintf(&b, "ranking candidates=%d role_fallback=%t", len(rk.Order), rk.RoleFallback)
	for i, r := range rk.Order {
		if i == 0 {
			b.WriteString("\n")
		} else {
			b.WriteString("\n\n")
		}
		b.WriteString(ExplainCandidate(r))
	}
	return b.String()
}
