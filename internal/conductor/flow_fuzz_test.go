// Purpose: fuzz target for ParseVerdict (§5 rule 7 fuzz-corpus requirement),
//   seeded from the golden fixtures. Asserts the fuzzer never panics and
//   never returns a Verdict outside the four enum values.
// SPORT: conductor.flow/ADD (P1-E11-W3-S23-T6).

package conductor

import "testing"

func FuzzParseVerdict(f *testing.F) {
	seeds := []string{
		"VERDICT: APPROVE",
		"VERDICT: REJECT",
		"VERDICT: NEEDS_CHANGES",
		"verdict: approve",
		"VERDICT: APPROVE\nVERDICT: REJECT",
		"I approve of this change.",
		"",
		"VERDICT: APPROVED",
		"  VERDICT: reject  ",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		got := ParseVerdict(raw)
		switch got {
		case VerdictUnknown, VerdictApprove, VerdictReject, VerdictNeedsChanges:
			// one of the four enum values, as required.
		default:
			t.Fatalf("ParseVerdict(%q) returned out-of-enum value %q", raw, got)
		}
	})
}
