package policy

import (
	"testing"
)

func TestHoldoutLeakPartialTokenNotMatched(t *testing.T) {
	// H-1045, xH-104, H-104_x and H-104-more do not match ID H-104;
	// whole token characters are ASCII letters/digits/underscore/hyphen
	id := "H-104"

	cases := []struct {
		proposal string
		want     bool // true if leak detected
	}{
		{"H-1045", false},
		{"xH-104", false},
		{"H-104_x", false},
		{"H-104-more", false},
		{"H-104", true},
		{"some text H-104 more text", true},
		{"H-104.", true},
		{".H-104", true},
	}

	for _, tc := range cases {
		tokens := tokenize(tc.proposal)
		found := false
		for _, tok := range tokens {
			if tok == id {
				found = true
				break
			}
		}
		if found != tc.want {
			t.Errorf("tokenize(%q) tokens=%v, expected leak=%v, got leak=%v", tc.proposal, tokens, tc.want, found)
		}
	}
}
