// Purpose: tests for `cascade fleet learn status [--json]` -- the argument
//
//	refusal, the actionable no-daemon refusal, the human rendering and the
//	--json wire shape of the learn.status reply.
//
// SPORT: cmd/cascade/fleet-learn-cli (ADD, P1-LRN-04).
package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestFleetLearnStatus_RejectsPositionalArgs proves status takes no
// positional argument.
func TestFleetLearnStatus_RejectsPositionalArgs(t *testing.T) {
	cmd := newFleetLearnStatusCmd(fleetSessionsDeps{})
	cmd.SetArgs([]string{"extra"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("fleet learn status with a positional arg: want an error, got nil")
	}
}

// TestFleetLearnStatus_NoDaemonActionable proves status refuses with the
// next step when no daemon is reachable.
func TestFleetLearnStatus_NoDaemonActionable(t *testing.T) {
	cmd := newFleetLearnStatusCmd(fleetSessionsDeps{})
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "cascade daemon run") {
		t.Fatalf("fleet learn status with no daemon = %v, want a refusal suggesting `cascade daemon run`", err)
	}
}

// TestFleetLearnStatus_JSONFlagIsGlobal proves --json resolves through the
// root's persistent flag that status reads, so the command needs no local
// duplicate of it.
func TestFleetLearnStatus_JSONFlagIsGlobal(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	if root.PersistentFlags().Lookup("json") == nil {
		t.Fatal("root has no persistent --json flag for fleet learn status to read")
	}
}

// TestFleetLearnStatus_Render proves the human rendering: an empty-state
// line, and one line per score row with its fields.
func TestFleetLearnStatus_Render(t *testing.T) {
	if got := (learnStatusResult{}).String(); got != "no learned scores" {
		t.Fatalf("empty status render = %q", got)
	}
	got := learnStatusResult{Scores: []learnScoreRow{
		{Tier: "T1", TaskClass: "code", Score: 0.5, Alpha: 2, Beta: 2, Observations: 4, FallbackLevel: "lang"},
		{Tier: "T2", TaskClass: "review", Score: 0.25, Alpha: 1, Beta: 3, Observations: 2, FallbackLevel: "global"},
	}}.String()
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("status render has %d lines, want 2: %q", len(lines), got)
	}
	want := "tier=T1 task_class=code score=0.500 alpha=2.00 beta=2.00 observations=4 fallback=lang"
	if lines[0] != want {
		t.Fatalf("first status line = %q, want %q", lines[0], want)
	}
}

// TestFleetLearnStatus_JSONShape proves the --json payload carries the
// scores array with the snake_case field names the daemon reply uses.
func TestFleetLearnStatus_JSONShape(t *testing.T) {
	raw, err := json.Marshal(learnStatusResult{Scores: []learnScoreRow{
		{Tier: "T1", TaskClass: "code", Score: 0.5, Alpha: 2, Beta: 2, Observations: 4, FallbackLevel: "repo"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Scores []map[string]any `json:"scores"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Scores) != 1 {
		t.Fatalf("scores = %d rows, want 1: %s", len(payload.Scores), raw)
	}
	for _, key := range []string{"tier", "task_class", "score", "alpha", "beta", "observations", "fallback_level"} {
		if _, ok := payload.Scores[0][key]; !ok {
			t.Errorf("score row lacks %q: %s", key, raw)
		}
	}
}
