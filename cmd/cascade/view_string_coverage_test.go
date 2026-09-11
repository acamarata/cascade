// Purpose: coverage-only additions for pure, branch-heavy functions
// (nodeListView.String, providerUsageResult.String, formatElapsed) whose
// less-common branch had no test exercising it — no production code
// changes, per this tree's own "coverage-only addition, no sport_updates"
// convention (daemon_unix_run_test.go). Added while wiring the SPORT
// entity registry doctor view, to keep cmd/cascade's ratchet baseline
// (testdata/coverage-baseline.json: 80.0%) satisfied after that addition.
//
// SPORT: cmd/cascade/node (coverage-only addition, no sport_updates —
//
//	nodeListView.String non-empty branch), cmd/cascade/provider (coverage-
//	only addition, no sport_updates — providerUsageResult.String non-empty
//	branch), cmd/cascade/fleet (coverage-only addition, no sport_updates —
//	formatElapsed's zero/future branches).
package main

import (
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestNodeListView_String_EmptyAndNonEmpty(t *testing.T) {
	if got := (nodeListView{}).String(); got != "no enrolled nodes" {
		t.Errorf("empty nodeListView.String() = %q, want %q", got, "no enrolled nodes")
	}
	v := nodeListView{Nodes: []nodeRowView{
		{NodeID: "n1", Tier: "trusted", Liveness: "online", Drained: false},
	}}
	if got := v.String(); !strings.Contains(got, "n1") {
		t.Errorf("non-empty nodeListView.String() = %q, want it to contain %q", got, "n1")
	}
}

func TestProviderUsageResult_String_EmptyAndNonEmpty(t *testing.T) {
	if got := (providerUsageResult{}).String(); got != "no usage recorded" {
		t.Errorf("empty providerUsageResult.String() = %q, want %q", got, "no usage recorded")
	}
	r := providerUsageResult{Rows: []provider.UsageSummary{
		{ProviderName: "anthropic", LaneName: "default", TokensIn: 10, TokensOut: 20, CostMicroUSD: 5},
	}}
	if got := r.String(); !strings.Contains(got, "anthropic") {
		t.Errorf("non-empty providerUsageResult.String() = %q, want it to contain %q", got, "anthropic")
	}
}

// TestFormatElapsed_AllBranches covers formatElapsed's three branches
// (zero timestamp, a future timestamp clamped to zero, and a real past
// duration) — previously only the past-duration branch had a test.
func TestFormatElapsed_AllBranches(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	clk := runtime.NewFixedClock(now)
	if got := formatElapsed(0, clk); got != "-" {
		t.Errorf("formatElapsed(0, ...) = %q, want %q", got, "-")
	}
	if got := formatElapsed(now.Unix()+3600, clk); got != "0s" {
		t.Errorf("formatElapsed(future, ...) = %q, want %q", got, "0s")
	}
	if got := formatElapsed(now.Unix()-90, clk); got != "1m30s" {
		t.Errorf("formatElapsed(past, ...) = %q, want %q", got, "1m30s")
	}
}
