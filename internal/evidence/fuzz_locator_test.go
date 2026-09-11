package evidence

// Purpose: FuzzLocatorParse, the 06 SS5 rule 7 fuzz target for
// ParseLocator's parser. Seed corpus at
// testdata/fuzz/FuzzLocatorParse/.
//
// SPORT: evidence/locator (ADD).

import "testing"

func FuzzLocatorParse(f *testing.F) {
	seeds := []string{
		"lines:1-10",
		"bytes:0-16384",
		"artifact://ART-abc123",
		"",
		"lines:",
		"lines:5-3",
		"lines:-1-5",
		"bytes:abc-def",
		"foo://bar",
		"artifact://",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		loc, err := ParseLocator(s)
		if err != nil {
			return
		}
		// A successful parse must round-trip through String() to a value
		// that reparses to the identical Locator -- the round-trip
		// guarantee ParseLocator/String's own doc comments make.
		reparsed, err := ParseLocator(loc.String())
		if err != nil {
			t.Fatalf("ParseLocator(%q) succeeded but re-parsing its own String() %q failed: %v", s, loc.String(), err)
		}
		if reparsed != loc {
			t.Fatalf("round trip mismatch: ParseLocator(%q) = %+v, reparsed = %+v", s, loc, reparsed)
		}
	})
}
