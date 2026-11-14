package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/learn"
)

// TestFleetLearnExplain_DecodesDaemonReply proves the CLI's mirror type
// decodes every field the daemon's learn.explain reply carries, so no
// R-21.175 column is dropped between the handler and the renderer.
func TestFleetLearnExplain_DecodesDaemonReply(t *testing.T) {
	reply := learn.ExplainResult{
		Scores: []learn.ScoreRow{{Tier: "T0", TaskClass: "code", Score: 0.75, Alpha: 3, Beta: 1,
			Observations: 2, FallbackLevel: "global"}},
		Tier0: &learn.ExplainEstimate{Tier: "T0", QueueWait: 2 * time.Second, DurationEst: time.Minute,
			PRework: 0.2, ReworkCyclesEst: 1, ExpectedTime: 3 * time.Minute},
		BestLower: &learn.ExplainEstimate{Tier: "T1", ExpectedTime: time.Minute},
		Threshold: 0.5,
		JumpFires: true,
	}
	raw, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	var got learnExplainResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Scores) != 1 || got.Scores[0] != (learnScoreRow{"T0", "code", 0.75, 3, 1, 2, "global"}) {
		t.Errorf("scores = %+v", got.Scores)
	}
	want := learnExplainEstimate{"T0", 2 * time.Second, time.Minute, 0.2, 1, 3 * time.Minute}
	if got.Tier0 == nil || *got.Tier0 != want {
		t.Errorf("tier0 = %+v, want %+v", got.Tier0, want)
	}
	if got.BestLower == nil || got.BestLower.Tier != "T1" || got.BestLower.ExpectedTime != time.Minute {
		t.Errorf("best_lower = %+v", got.BestLower)
	}
	if got.Threshold != 0.5 || !got.JumpFires {
		t.Errorf("threshold/jump = %v/%v", got.Threshold, got.JumpFires)
	}
}

// TestFleetLearnExplain_ParamsShape proves the request body names the two
// fields the learn.explain handler decodes.
func TestFleetLearnExplain_ParamsShape(t *testing.T) {
	raw, err := json.Marshal(learnExplainParams{ScopeKey: "global", TaskClass: "code"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != `{"scope_key":"global","task_class":"code"}` {
		t.Fatalf("explain params = %s", got)
	}
}
