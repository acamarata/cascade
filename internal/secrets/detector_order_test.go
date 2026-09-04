package secrets

// Purpose: the hit ranking order, exercised rung by rung. Determinism is
//
//	only as good as its tie-breaks, and the corpus alone exercises two of
//	the five, so the rest are covered directly here.

import "testing"

// TestHitOrderIsTotal exercises every tie-break rung of the ranking
// order, so the documented determinism does not rest on the two cases the
// corpus happens to produce.
func TestHitOrderIsTotal(t *testing.T) {
	base := DetectionHit{Class: ClassAPIKey, Pattern: "a", Offset: 10, Len: 5, Confidence: ConfidenceCertain}
	cases := []struct {
		name string
		a, b DetectionHit
		want bool
	}{
		{"higher confidence first", base, withConfidence(base, ConfidenceWeak), true},
		{"lower confidence second", withConfidence(base, ConfidenceWeak), base, false},
		{"earlier offset first", base, withOffset(base, 20), true},
		{"longer span first", base, withLen(base, 2), true},
		{"class breaks the tie", base, withClass(base, ClassJWT), true},
		{"pattern breaks the last tie", base, withPattern(base, "b"), true},
		{"identical hits are not ordered", base, base, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hitBefore(tc.a, tc.b); got != tc.want {
				t.Fatalf("hitBefore = %v, want %v", got, tc.want)
			}
		})
	}
	if resolveOverlaps(nil) != nil {
		t.Error("resolveOverlaps(nil) returned a non-nil slice")
	}
}

func withConfidence(h DetectionHit, c Confidence) DetectionHit { h.Confidence = c; return h }
func withOffset(h DetectionHit, o int) DetectionHit            { h.Offset = o; return h }
func withLen(h DetectionHit, l int) DetectionHit               { h.Len = l; return h }
func withClass(h DetectionHit, c Class) DetectionHit           { h.Class = c; return h }
func withPattern(h DetectionHit, p string) DetectionHit        { h.Pattern = p; return h }
