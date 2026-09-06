package eval

// Purpose: FuzzEvalFixture — decoder fuzzing over
//   LoadRecordedEmbeddings, the richest validation path in this package
//   (Art.2 provenance, dimension agreement, finite-vector checks), per
//   06-FORGE-SPEC.md §5 rule 7.
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: R-21.266 — the seed corpus lives package-local at
//   testdata/fuzz/FuzzEvalFixture/, which `go test -fuzz` auto-loads; no
//   f.Add ceremony. Art.7 — no filesystem writes, no network.
// SPORT: placeholder: retrieval/evaluation (ADD, P1-E06-W2-S12-T6).

import (
	"bytes"
	"testing"
)

// FuzzEvalFixture must never panic or hang on arbitrary bytes: every
// malformed input is required to come back as an error, never a crash.
func FuzzEvalFixture(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = LoadRecordedEmbeddings(bytes.NewReader(data))
	})
}
