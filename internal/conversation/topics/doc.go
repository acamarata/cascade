// Package topics holds the topic-engine's fixture corpus and evaluation
// harness (P1-E21-W5-S45-T1). It defines the corpus record format, a
// load-and-validate function with typed error paths, and scorers that
// compute boundary-detection F1 and topic-assignment accuracy against the
// floors set by 06-FORGE-SPEC §5 rule 12 (boundary F1 >=0.80, assignment
// accuracy >=0.85).
//
// The corpus itself is an owner-supplied sanitized labeled transcript
// corpus (06-FORGE-SPEC §7 owner prerequisite, shared by L/S-25.T1,
// U/S-45.T1 and F/S-12.T5). It is not present in this tree as of this
// ticket: see testdata/corpus/README.md for the delivery requirements and
// eval_corpus_test.go for how the accuracy-floor test skips cleanly in its
// absence.
//
// The segmenter, hysteresis filter, and topic stack that produce real
// predictions are out of scope here and land in T2, which consumes
// EvalResult, Evaluate, and AssertFloors from this package.
package topics
