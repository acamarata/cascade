package eval_test

// Purpose: TestFusionGate, over a real temporary FTS5 index and the real
//   S-11.T1 RRF fusion, plus the mutation proof this ticket exists to
//   demonstrate: the gate is not vacuously green. Dropping the vector leg
//   must turn a measured PASS red, and restoring it must turn it green
//   again, with the metric values reported both ways.
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: Art.7 — FTS5 database only below t.TempDir(), no network.
// SPORT: placeholder: retrieval/evaluation (ADD, P1-E06-W2-S12-T6).

import (
	"context"
	"os"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/eval"
	"github.com/acamarata/cascade/internal/runtime"
)

// runGate runs both query sets through legs and measures the gate.
func runGate(t *testing.T, knownItem, semantic eval.QuerySet, legs eval.Legs) eval.GateVerdict {
	t.Helper()
	kiResults, err := eval.RunKnownItem(context.Background(), knownItem, legs)
	if err != nil {
		t.Fatalf("RunKnownItem: %v", err)
	}
	semResults, err := eval.RunSemanticParaphrase(context.Background(), semantic, legs)
	if err != nil {
		t.Fatalf("RunSemanticParaphrase: %v", err)
	}
	verdict, err := eval.FusionDefaultGate(semantic.Queries, semResults, knownItem.Queries, kiResults)
	if err != nil {
		t.Fatalf("FusionDefaultGate: %v", err)
	}
	return verdict
}

// TestFusionGate is this ticket's acceptance measurement over the real
// committed corpus: a real temporary FTS5 SQLite index (S-10.T2), the
// real S-11.T1 vector leg driven by the committed recorded-embedder
// fixture, and the real S-11.T1 RRF fusion. Measured today: FTS5-only
// semantic/paraphrase recall@10 = 1/6 (only one paraphrase happens to
// share literal tokens with its document; the other five share none,
// which is the point of a paraphrase set against a phrase/conjunctive
// lexical leg — see fts5_query.go), fused recall@10 = 6/6, a 500%
// relative lift, and no known-item loss: PASS.
func TestFusionGate(t *testing.T) {
	fixtureCorpus, knownItem, semantic, embeddings := loadEvalFixtures(t)
	legs := buildEvalHarness(t, fixtureCorpus, embeddings)

	verdict := runGate(t, knownItem, semantic, legs)
	t.Logf("measured verdict: %+v", verdict)

	if !verdict.Pass {
		t.Fatalf("gate did not pass over the real fixtures: %+v", verdict)
	}
	if verdict.RelativeLift < 0.10 {
		t.Errorf("relative lift = %v, want >= 0.10", verdict.RelativeLift)
	}
	if !verdict.KnownItemLossFree {
		t.Error("known-item loss reported on a passing gate")
	}
}

// TestFusionGate_MutationProof is the demonstration this ticket exists to
// make honest: the gate above is not a gate that can only ever pass.
// Dropping the real vector leg — the same real pipeline, one real
// collaborator removed — collapses the fused ranking to FTS5-only, so
// the relative lift the gate requires becomes exactly zero, and the gate
// must go RED. Restoring the vector leg must reproduce the exact PASS
// verdict TestFusionGate asserts, proving the gate is sensitive to the
// fusion it is meant to grade rather than to something else in the
// harness.
func TestFusionGate_MutationProof(t *testing.T) {
	fixtureCorpus, knownItem, semantic, embeddings := loadEvalFixtures(t)
	legs := buildEvalHarness(t, fixtureCorpus, embeddings)

	green := runGate(t, knownItem, semantic, legs)
	t.Logf("green (real fusion): Pass=%v lift=%v fts5=%v fused=%v",
		green.Pass, green.RelativeLift, green.SemanticFTS5Recall, green.SemanticFusedRecall)
	if !green.Pass {
		t.Fatalf("baseline (unperturbed) gate did not pass: %+v", green)
	}

	perturbed := legs
	perturbed.Vector = nil // drop the vector leg: fusion degrades to FTS5-only.
	red := runGate(t, knownItem, semantic, perturbed)
	t.Logf("red (vector leg dropped): Pass=%v lift=%v fts5=%v fused=%v",
		red.Pass, red.RelativeLift, red.SemanticFTS5Recall, red.SemanticFusedRecall)
	if red.Pass {
		t.Fatalf("gate still passed with the vector leg dropped: %+v", red)
	}
	if red.RelativeLift != 0 {
		t.Errorf("with no vector leg, fused == fts5-only, so lift must be exactly 0; got %v", red.RelativeLift)
	}
	if red.SemanticFTS5Recall != green.SemanticFTS5Recall {
		t.Errorf("FTS5-only recall changed across the perturbation (%v -> %v); the perturbation must isolate the vector leg",
			green.SemanticFTS5Recall, red.SemanticFTS5Recall)
	}

	restored := runGate(t, knownItem, semantic, legs)
	t.Logf("restored: Pass=%v lift=%v", restored.Pass, restored.RelativeLift)
	if !restored.Pass {
		t.Fatalf("gate did not return to PASS after the vector leg was restored: %+v", restored)
	}
	if restored != green {
		t.Errorf("restored verdict %+v does not match the original green verdict %+v", restored, green)
	}
}

// TestFusionGate_ZeroDenominatorIsTypedFailure proves the gate never
// treats an unmeasurable input as a permissive pass: a semantic/
// paraphrase set the FTS5-only leg matches nothing at all in produces a
// typed error, not Pass=true and not Pass=false-with-a-zero-value.
func TestFusionGate_ZeroDenominatorIsTypedFailure(t *testing.T) {
	corpus, err := eval.NewEvalCorpus([]eval.Document{{ID: "a", Text: "alpha"}})
	if err != nil {
		t.Fatalf("NewEvalCorpus: %v", err)
	}
	queries := []eval.Query{{Text: "q", ExpectedIDs: []string{"a"}}}
	fts5Zero := []eval.RunResult{{
		FTS5Only: eval.RankedResult{QueryText: "q", RankedIDs: nil},
		Fused:    eval.RankedResult{QueryText: "q", RankedIDs: []string{"a"}},
	}}
	knownItemOK := []eval.RunResult{{
		FTS5Only: eval.RankedResult{QueryText: "q", RankedIDs: []string{"a"}},
		Fused:    eval.RankedResult{QueryText: "q", RankedIDs: []string{"a"}},
	}}
	_, err = eval.FusionDefaultGate(queries, fts5Zero, queries, knownItemOK)
	if err == nil {
		t.Fatal("zero fts5-only denominator: want a typed error, never a permissive verdict")
	}
	_ = corpus
}

// TestV1August2026Baseline refuses a regression against the harvested v1
// August-2026 fixture. See Baseline's doc comment and testdata/README.md
// for why the comparison is the fusion/FTS5-only known-item RATIO rather
// than an absolute recall@10 value: v1's real fixture measured MRR@10 on
// a different corpus with a mock-embedder vector leg, so only the
// dimensionless ratio transfers honestly.
func TestV1August2026Baseline(t *testing.T) {
	f, err := os.Open("testdata/v1-goldens/august-2026-baseline.json")
	if err != nil {
		t.Fatalf("open baseline: %v", err)
	}
	defer func() { _ = f.Close() }()
	baseline, err := eval.LoadBaseline(f)
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}

	fixtureCorpus, knownItem, _, embeddings := loadEvalFixtures(t)
	legs := buildEvalHarness(t, fixtureCorpus, embeddings)
	results, err := eval.RunKnownItem(context.Background(), knownItem, legs)
	if err != nil {
		t.Fatalf("RunKnownItem: %v", err)
	}
	fts5Recall, err := eval.RecallAt10(knownItem.Queries, fts5OnlyResults(results))
	if err != nil {
		t.Fatalf("RecallAt10 (fts5-only): %v", err)
	}
	fusedRecall, err := eval.RecallAt10(knownItem.Queries, fusedResults(results))
	if err != nil {
		t.Fatalf("RecallAt10 (fused): %v", err)
	}
	if fts5Recall == 0 {
		t.Fatal("current fts5-only known-item recall@10 is zero; the ratio is undefined")
	}
	currentRatio := fusedRecall / fts5Recall
	baselineRatio := baseline.FusionToFTS5Ratio()
	const tolerance = 0.01
	if currentRatio < baselineRatio-tolerance {
		t.Errorf("known-item fusion/fts5-only ratio regressed: current %.4f (fts5=%.4f fused=%.4f) < v1 baseline %.4f (%s)",
			currentRatio, fts5Recall, fusedRecall, baselineRatio, baseline.Metric)
	}
}

func fusedResults(results []eval.RunResult) []eval.RankedResult {
	out := make([]eval.RankedResult, len(results))
	for i, r := range results {
		out[i] = r.Fused
	}
	return out
}

// TestRetrievalFusionEnabledDefault recomputes the R-16.9 gate verdict
// from the committed fixtures and asserts it agrees with
// runtime.DefaultFusionEnabled. This is the real half of the cross-check:
// internal/retrieval/eval can safely import internal/runtime (runtime
// imports nothing back), which is why the recomputation lives here rather
// than in internal/runtime — see that package's own
// TestRetrievalFusionEnabledDefault (config_test.go) for the import-cycle
// reason and what it asserts instead.
func TestRetrievalFusionEnabledDefault(t *testing.T) {
	fixtureCorpus, knownItem, semantic, embeddings := loadEvalFixtures(t)
	legs := buildEvalHarness(t, fixtureCorpus, embeddings)
	verdict := runGate(t, knownItem, semantic, legs)
	if verdict.Pass != runtime.DefaultFusionEnabled {
		t.Fatalf("measured gate verdict Pass=%v disagrees with runtime.DefaultFusionEnabled=%v (R-16.9): %+v",
			verdict.Pass, runtime.DefaultFusionEnabled, verdict)
	}
}
