// Package topics holds the topic-engine: the segmenter (Segmenter,
// segmenter_core.go) that finds topic boundaries across an ordered window
// of turns (P1-E21-W5-S45-T2), plus the fixture corpus and evaluation
// harness (P1-E21-W5-S45-T1) used to measure it. The harness - the corpus
// record format, a load-and-validate function with typed error paths, and
// scorers that compute boundary-detection F1 and topic-assignment accuracy
// against the floors set by 06-FORGE-SPEC §5 rule 12 (boundary F1 >=0.80,
// assignment accuracy >=0.85) - is test-only (eval_harness_test.go, moved
// there from eval.go under R-14.283, 2026-09-20: it has no production
// caller and none is planned, so it is a measurement a test performs, not
// package API).
//
// The corpus itself is an owner-supplied sanitized labeled transcript
// corpus (06-FORGE-SPEC §7 owner prerequisite, shared by L/S-25.T1,
// U/S-45.T1 and F/S-12.T5). It is not present in this tree as of this
// ticket: see testdata/corpus/README.md for the delivery requirements and
// eval_corpus_test.go for how the accuracy-floor test skips cleanly in its
// absence.
//
// The segmenter, hysteresis filter, and topic stack that produce real
// predictions land in T2 (segmenter_core.go, hysteresis.go, topic_stack.go)
// and are scored against the harness by segmenter_harness_test.go
// (untagged, synthetic corpus) and segmenter_corpus_test.go (tagged
// topics_corpus, owner corpus).
package topics
