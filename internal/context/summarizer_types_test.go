package context

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestGranularityValidAndString exercises the closed three-member
// Granularity vocabulary (R-14.61): the zero value and every out-of-range
// value must read as invalid, and every declared member must round-trip
// through String() to its named form.
func TestGranularityValidAndString(t *testing.T) {
	cases := []struct {
		name  string
		level Granularity
		valid bool
		want  string
	}{
		{"zero value", Granularity(0), false, "invalid-granularity"},
		{"turn-window", GranularityTurnWindow, true, "turn-window"},
		{"thread", GranularityThread, true, "thread"},
		{"epoch", GranularityEpoch, true, "epoch"},
		{"above range", Granularity(4), false, "invalid-granularity"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.level.Valid(); got != tc.valid {
				t.Errorf("Granularity(%d).Valid() = %v, want %v", tc.level, got, tc.valid)
			}
			if got := tc.level.String(); got != tc.want {
				t.Errorf("Granularity(%d).String() = %q, want %q", tc.level, got, tc.want)
			}
		})
	}
}

// TestDefaultWindowTokens asserts each granularity's code-default source
// window (R-14.62: no [context.summarizer.*] config keys) grows with scope
// -- turn-window < thread < epoch -- and an invalid level returns 0 rather
// than indexing out of range.
func TestDefaultWindowTokens(t *testing.T) {
	cases := []struct {
		level Granularity
		want  int
	}{
		{GranularityTurnWindow, 4000},
		{GranularityThread, 16000},
		{GranularityEpoch, 64000},
		{Granularity(0), 0},
		{Granularity(9), 0},
	}
	for _, tc := range cases {
		if got := DefaultWindowTokens(tc.level); got != tc.want {
			t.Errorf("DefaultWindowTokens(%v) = %d, want %d", tc.level, got, tc.want)
		}
	}
	turnWindow, thread, epoch := DefaultWindowTokens(GranularityTurnWindow), DefaultWindowTokens(GranularityThread), DefaultWindowTokens(GranularityEpoch)
	if turnWindow >= thread || thread >= epoch {
		t.Errorf("DefaultWindowTokens must grow strictly with scope: got turn-window=%d thread=%d epoch=%d", turnWindow, thread, epoch)
	}
}

// TestMaxSummaryTokens pins each level's summary bound to the exact numbers
// summarizer_types.go's derivation comment claims, and asserts the bound
// SHRINKS as the scope widens: a wider scope is asked for when there is
// less room, so it must answer in less text.
func TestMaxSummaryTokens(t *testing.T) {
	cases := []struct {
		level Granularity
		want  int
	}{
		{GranularityTurnWindow, 500},
		{GranularityThread, 250},
		{GranularityEpoch, 125},
		{Granularity(0), 0},
		{Granularity(9), 0},
	}
	for _, tc := range cases {
		if got := MaxSummaryTokens(tc.level); got != tc.want {
			t.Errorf("MaxSummaryTokens(%v) = %d, want %d", tc.level, got, tc.want)
		}
	}
	if MaxSummaryTokens(GranularityTurnWindow) <= MaxSummaryTokens(GranularityThread) ||
		MaxSummaryTokens(GranularityThread) <= MaxSummaryTokens(GranularityEpoch) {
		t.Error("MaxSummaryTokens must shrink strictly as scope widens")
	}
}

// TestSummarizerBudgetThresholdsMatchTheirDerivation ties the two literal
// thresholds back to the tables they were derived from. The constants are
// written as literals on purpose (see their comment), so this is the check
// that a change to a window size or a compression divisor cannot silently
// leave the selection boundaries behind.
func TestSummarizerBudgetThresholdsMatchTheirDerivation(t *testing.T) {
	if budgetForTurnWindow != MaxSummaryTokens(GranularityTurnWindow) {
		t.Errorf("budgetForTurnWindow = %d, want MaxSummaryTokens(turn-window) = %d",
			budgetForTurnWindow, MaxSummaryTokens(GranularityTurnWindow))
	}
	if budgetForThread != MaxSummaryTokens(GranularityThread) {
		t.Errorf("budgetForThread = %d, want MaxSummaryTokens(thread) = %d",
			budgetForThread, MaxSummaryTokens(GranularityThread))
	}
	if budgetForThread >= budgetForTurnWindow {
		t.Errorf("thresholds must descend: budgetForThread=%d budgetForTurnWindow=%d", budgetForThread, budgetForTurnWindow)
	}
	// The level a threshold selects must be able to answer within it.
	if MaxSummaryTokens(GranularityEpoch) > budgetForThread {
		t.Errorf("the epoch bound %d does not fit the budgets that select it (below %d)",
			MaxSummaryTokens(GranularityEpoch), budgetForThread)
	}
}

// TestSelectGranularity walks the whole selection ladder, including both
// boundaries and a non-positive budget.
func TestSelectGranularity(t *testing.T) {
	cases := []struct {
		budget int
		want   Granularity
	}{
		{100000, GranularityTurnWindow},
		{budgetForTurnWindow, GranularityTurnWindow},
		{budgetForTurnWindow - 1, GranularityThread},
		{budgetForThread, GranularityThread},
		{budgetForThread - 1, GranularityEpoch},
		{1, GranularityEpoch},
		{0, GranularityEpoch},
		{-50, GranularityEpoch},
	}
	for _, tc := range cases {
		if got := SelectGranularity(tc.budget); got != tc.want {
			t.Errorf("SelectGranularity(%d) = %v, want %v", tc.budget, got, tc.want)
		}
	}
}

// TestSummarizerPersistenceRoundTrip: a summary written by one Summarizer
// instance (via a SummaryRecord persisted to the store) is readable,
// unchanged, by a FRESH Summarizer instance sharing the same Store -- and
// the fresh instance's own executor is never called for a matching-version
// read (R-14.62's round-trip acceptance criterion).
func TestSummarizerPersistenceRoundTrip(t *testing.T) {
	store := storetest.NewMemStore()
	writer := newTestSummarizer(t, &fakeExecutor{output: "persisted summary"}, store)
	ctx := context.Background()

	written, err := writer.GetSummary(ctx, "entity-rt", GranularityEpoch, "source", "v1")
	if err != nil {
		t.Fatalf("writer GetSummary: %v", err)
	}

	readerExec := &fakeExecutor{}
	reader := newTestSummarizer(t, readerExec, store)
	read, err := reader.GetSummary(ctx, "entity-rt", GranularityEpoch, "source", "v1")
	if err != nil {
		t.Fatalf("reader GetSummary: %v", err)
	}
	if readerExec.callCount() != 0 {
		t.Errorf("reader dispatched %d model.execute calls, want 0 (round-trip must be a pure storage read)", readerExec.callCount())
	}
	if read.Record.Content != written.Record.Content || read.Record.SourceVersion != written.Record.SourceVersion {
		t.Errorf("round-tripped record = %+v, want it to match the written record %+v", read.Record, written.Record)
	}
	if read.Record.Level != GranularityEpoch {
		t.Errorf("round-tripped Level = %v, want epoch", read.Record.Level)
	}
}
