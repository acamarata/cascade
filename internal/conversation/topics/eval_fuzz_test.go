// Package topics (eval_fuzz_test.go): Purpose: FuzzCorpusRecord exercises
// LoadCorpusRecord, the corpus record load-and-validate function, per
// 06-FORGE-SPEC §5 rule 7 (every parser/decoder ticket carries a fuzz
// target).
//
// Inputs: arbitrary []byte, seeded from
// testdata/fuzz/FuzzCorpusRecord/seed001.
//
// Outputs: none; the property under test is "never panics", which the Go
// fuzzer enforces by construction. A returned error is an expected,
// non-failing outcome for malformed input.
//
// Constraints: no network, no filesystem writes outside the fuzz corpus Go
// itself manages.
package topics

import "testing"

// FuzzCorpusRecord fuzzes LoadCorpusRecord with arbitrary bytes, seeded
// from a minimal valid record. LoadCorpusRecord must return an error for
// malformed input rather than panicking.
func FuzzCorpusRecord(f *testing.F) {
	f.Add([]byte(`{"id":"seed","turns":[{"speaker":"a","text":"hi"}],"boundaries":[],"topic_labels":{}}`))
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = LoadCorpusRecord(data)
	})
}
