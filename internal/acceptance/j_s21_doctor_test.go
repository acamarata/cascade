//go:build integration

package acceptance

// Purpose (this file): pins how narrow step 6's tolerance is. The drill
//   accepts exactly one non-OK doctor line, an elevation_custody WARN in
//   one of P1-SEC-12's contracted forms; these cases prove every other
//   shape still fails, so the tolerance cannot widen unnoticed.
// SPORT: acceptance/j-s21-compat-sub (ADD) — P1-E10-W3-S21-T3.

import "testing"

func TestJ_S21_DoctorCustodyBoundaryIsNarrow(t *testing.T) {
	const (
		none     = "WARN   elevation_custody              tier=none: no protected source available; elevated verbs refuse"
		unbound  = "WARN   elevation_custody              tier=platform (source platform) but no enrolled key is bound to it: x; elevated verbs refuse"
		presence = "WARN   elevation_custody              tier=presence (source presence): weaker than hardware custody. residual. fingerprint=SHA256:fake"
		unboundP = "WARN   elevation_custody              tier=presence (source presence): weaker than hardware custody. residual. The enrolment is not bound to the presence key: x"
		changed  = "WARN   elevation_custody              tier=presence (source presence): weaker than hardware custody. residual. The enrolled fingerprint a differs from the last audited enrolment b."
		fresh    = "WARN   elevation_custody              tier=presence (source presence): weaker than hardware custody. residual. A fresh enrolment b was audited after the earlier enrolment a (the key was deleted and enrolled again)."
		errStat  = "ERROR  elevation_custody              tier=none: elevated verbs refuse"
		other    = "WARN   provider_health                provider_health: elevated verbs refuse"
	)
	cases := []struct {
		line    string
		granted bool
		want    bool
	}{
		{none, false, true},
		{unbound, false, true},
		{presence, false, true},
		{presence, true, true},
		{none, true, false},      // step 3 elevated, so a refusal is a contradiction
		{unbound, true, false},   // same
		{unboundP, false, false}, // unbound presence key: a real regression
		{changed, true, false},   // the key-replacement signal
		{fresh, true, false},     // delete-and-re-enrol signal
		{errStat, false, false},
		{other, false, false},
	}
	for _, c := range cases {
		if got := custodyBoundary(c.line, c.granted); got != c.want {
			t.Errorf("custodyBoundary(%q, granted=%v) = %v, want %v", c.line, c.granted, got, c.want)
		}
	}

	out := "OK     audit_chain                    audit chain verified\n" + none + "\n" + other + "\n"
	if got := doctorProblems(out); len(got) != 2 {
		t.Errorf("doctorProblems found %d problem lines, want 2: %q", len(got), got)
	}
}
