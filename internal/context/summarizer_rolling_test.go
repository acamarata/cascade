package context

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: the three behaviours that make these "three-granularity ROLLING
//   summaries" rather than three independent one-shot summarizers: that the
//   remaining budget selects the level, that each regeneration folds the
//   PRIOR summary into the new window, and that an over-long response is
//   refused rather than stored.
// SPORT: context-engine/summarizer-rolling-test (ADD, P1-E21-W5-S46-T2).

// storedRecord reads the persisted SummaryRecord for one {entityID, level}
// storage key, or reports that none exists.
func storedRecord(t *testing.T, store provider.Store, entityID string, level Granularity) (SummaryRecord, bool) {
	t.Helper()
	raw, err := store.Get(context.Background(), summarizerNamespace, summaryKey(entityID, level))
	if cascade.HasKind(err, cascade.KindNotFound) {
		return SummaryRecord{}, false
	}
	if err != nil {
		t.Fatalf("store.Get(%s/%s): %v", entityID, level, err)
	}
	var rec SummaryRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("decoding stored record: %v", err)
	}
	return rec, true
}

// TestSummarizeSelectsGranularityFromBudget drives Summarize at three
// budgets and asserts each one reaches a DIFFERENT granularity: the level is
// chosen from what is left of the budget, so all three R-14.61 levels are
// reachable through the seam the Composer actually calls.
func TestSummarizeSelectsGranularityFromBudget(t *testing.T) {
	cases := []struct {
		name   string
		budget int
		want   Granularity
	}{
		{"a large budget affords the least compressed level", 4000, GranularityTurnWindow},
		{"a middling budget takes the thread level", 300, GranularityThread},
		{"a tight budget needs the most compressed level", 60, GranularityEpoch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &fakeExecutor{output: "a summary."}
			store := storetest.NewMemStore()
			s := newTestSummarizer(t, exec, store)

			if _, err := s.Summarize(context.Background(), Slot{Label: "e", Content: "some source"}, tc.budget); err != nil {
				t.Fatalf("Summarize(budget=%d): %v", tc.budget, err)
			}
			assertLevelChosen(t, exec, store, tc.want)
		})
	}
}

// assertLevelChosen asserts want is the level the one dispatched request
// named AND the level the stored record was keyed under, and that neither
// other level was touched.
func assertLevelChosen(t *testing.T, exec *fakeExecutor, store provider.Store, want Granularity) {
	t.Helper()
	if body := exec.requestAt(0).Inputs[0].Content; !strings.Contains(body, want.String()) {
		t.Errorf("dispatched prompt does not name the %s level: %q", want, body)
	}
	rec, found := storedRecord(t, store, "e", want)
	if !found {
		t.Fatalf("no record stored under the %s key", want)
	}
	if rec.Level != want {
		t.Errorf("stored Level = %v, want %v", rec.Level, want)
	}
	for _, other := range []Granularity{GranularityTurnWindow, GranularityThread, GranularityEpoch} {
		if other == want {
			continue
		}
		if _, found := storedRecord(t, store, "e", other); found {
			t.Errorf("a record was also stored under the %s key, want only %s", other, want)
		}
	}
}

// TestSummarizerRollsPriorSummaryIntoTheNextRequest is the rolling property:
// a regeneration over new, DISJOINT content must be asked to update the
// summary already on record, not to summarize the new window alone.
// Otherwise every regeneration silently discards everything that has fallen
// out of the window since the last one.
func TestSummarizerRollsPriorSummaryIntoTheNextRequest(t *testing.T) {
	exec := &fakeExecutor{output: "SUMMARY-ONE covering the original material"}
	s := newTestSummarizer(t, exec, storetest.NewMemStore())
	ctx := context.Background()

	if _, err := s.GetSummary(ctx, "e", GranularityThread, "the original conversation tail", "v1"); err != nil {
		t.Fatalf("first GetSummary: %v", err)
	}
	if first := exec.requestAt(0).Inputs[0].Content; strings.Contains(first, "PRIOR SUMMARY") {
		t.Errorf("the first regeneration has no prior summary to fold in, yet its prompt names one: %q", first)
	}

	exec.output = "SUMMARY-TWO"
	if _, err := s.GetSummary(ctx, "e", GranularityThread, "DISJOINT-HEAD entirely new material", "v2"); err != nil {
		t.Fatalf("second GetSummary: %v", err)
	}
	second := exec.requestAt(1).Inputs[0].Content
	if !strings.Contains(second, "SUMMARY-ONE covering the original material") {
		t.Errorf("the second regeneration's input does not carry the prior summary: %q", second)
	}
	if !strings.Contains(second, "DISJOINT-HEAD entirely new material") {
		t.Errorf("the second regeneration's input does not carry the new material: %q", second)
	}
	if !strings.Contains(second, "PRIOR SUMMARY") {
		t.Errorf("the second regeneration's input does not instruct an update of the prior summary: %q", second)
	}
}

// TestSummarizerOversizeOutputIsNotPersisted asserts a model response above
// the level's own summary bound is refused: it is never written to the store
// and never served, and the failure names the size that broke the bound.
// Storing it would put a block the composer cannot afford into the store and
// hand it back on every later compose.
func TestSummarizerOversizeOutputIsNotPersisted(t *testing.T) {
	const level = GranularityThread
	bound := MaxSummaryTokens(level)
	exec := &fakeExecutor{output: strings.Repeat("x", (bound+2)*4)}
	store := storetest.NewMemStore()
	s := newTestSummarizer(t, exec, store)

	outcome, err := s.GetSummary(context.Background(), "e", level, "content", "v1")
	if err != nil {
		t.Fatalf("GetSummary: want the degrade-gracefully path, got error %v", err)
	}
	if outcome.Failed == nil {
		t.Fatal("outcome.Failed = nil, want a SummarizationFailedEvent for the oversize response")
	}
	if !strings.Contains(outcome.Failed.Reason, "size") || !strings.Contains(outcome.Failed.Reason, "252") {
		t.Errorf("Failed.Reason = %q, want it to name the response size that broke the bound", outcome.Failed.Reason)
	}
	if outcome.Record.Content != "" {
		t.Errorf("Record.Content = %q, want nothing served for an oversize response", outcome.Record.Content)
	}
	if _, found := storedRecord(t, store, "e", level); found {
		t.Error("an oversize model response was persisted; it must never reach the store")
	}
}

// TestSummarizerOutputExactlyAtTheBoundIsAccepted pins the comparison as
// "greater than the bound", not "greater than or equal": a response that
// spends its whole allowance is a valid summary.
func TestSummarizerOutputExactlyAtTheBoundIsAccepted(t *testing.T) {
	const level = GranularityThread
	exec := &fakeExecutor{output: strings.Repeat("x", MaxSummaryTokens(level)*4)}
	store := storetest.NewMemStore()
	s := newTestSummarizer(t, exec, store)

	outcome, err := s.GetSummary(context.Background(), "e", level, "content", "v1")
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}
	if outcome.Failed != nil {
		t.Fatalf("outcome.Failed = %+v, want a response exactly at the bound to be accepted", outcome.Failed)
	}
	if _, found := storedRecord(t, store, "e", level); !found {
		t.Error("a response exactly at the bound was not persisted")
	}
}
