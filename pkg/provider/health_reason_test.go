package provider

import "testing"

// TestDemotionReasonVocabulary pins the closed six-member vocabulary,
// including the transient_upstream member the dispatch counter states.
func TestDemotionReasonVocabulary(t *testing.T) {
	valid := map[DemotionReason]string{
		ReasonRateLimited429:    "rate_limited_429",
		ReasonDeadKeyHard:       "dead_key_hard",
		ReasonDeadKeySoft:       "dead_key_soft",
		ReasonIntakeFail:        "intake_fail",
		ReasonCapabilityDenied:  "capability_denied",
		ReasonTransientUpstream: "transient_upstream",
	}
	for r, want := range valid {
		if string(r) != want || !r.Valid() {
			t.Errorf("reason %q: want value %q and Valid", r, want)
		}
	}
	for _, bad := range []DemotionReason{"", "transient", "TRANSIENT_UPSTREAM"} {
		if bad.Valid() {
			t.Errorf("reason %q must not be valid", bad)
		}
	}
}
