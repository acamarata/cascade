package tailer

// Purpose: FuzzTranscriptParse exercises the versioned line parser
//   against arbitrary byte input, proving it never panics regardless of
//   how malformed the input is (06-FORGE-SPEC §5.7 fuzz requirement for
//   parsers/decoders).
// Inputs: arbitrary []byte, seeded from testdata/fuzz/FuzzTranscriptParse/
//   (package-local, Go-native corpus format, auto-loaded — R-21.266, no
//   f.Add ceremony).
// Constraints: the seed corpus carries no harness-captured bytes
//   (R-40.X16); this target's only assertion is "does not panic and
//   returns a well-formed (rawEvent, error) pair" — it is not a
//   round-trip or golden-value test.
// SPORT: fleet/tailer (ADD, per T-2 sport_updates).

import "testing"

// FuzzTranscriptParse fuzzes both harness dispatch paths' line parser
// against arbitrary bytes.
func FuzzTranscriptParse(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, h := range []Harness{HarnessCC, HarnessCodex} {
			p := newParser(h)
			_, err := p.parseLine(data, 1)
			if err != nil {
				var pe *ParseError
				if !asParseError(err, &pe) {
					t.Fatalf("parseLine(%q) returned a non-ParseError error: %v", h, err)
				}
			}
		}
	})
}

// asParseError is a tiny errors.As wrapper kept local to the fuzz test so
// this file has no non-test import beyond "testing".
func asParseError(err error, target **ParseError) bool {
	pe, ok := err.(*ParseError)
	if !ok {
		return false
	}
	*target = pe
	return true
}
