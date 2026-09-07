//go:build integration

// Purpose: the §5.12 fixture-provenance accuracy harness (HOW step 4):
//
//	load the owner-provided labeled corpus and assert
//	StateMachine.Advance classifies it at >=0.90 accuracy.
//
// Inputs: internal/fleet/sessions/testdata/corpus/*.json, each one
//
//	labeled record per this package's testdata/corpus/README.md schema.
//
// Outputs: a pass/fail/skip test result; on failure, the observed
//
//	accuracy is printed.
//
// Constraints: SKIPS (never fails) when the corpus directory carries no
//
//	*.json record files - the owner prereq is unmet as of this ticket's
//	run (see testdata/corpus/README.md), and the unit-test suite (this
//	file's own package, minus this integration-tagged file) must still
//	pass in full regardless. A malformed record file fails the run
//	closed rather than being silently skipped, since a corpus record
//	that cannot be parsed is not evidence of anything.
//
// SPORT: fleet/session-corpus (ADD, per T-1 sport_updates).
package sessions_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/census"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/fleet/tailer"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const corpusDir = "testdata/corpus"

// corpusRecord is one labeled fixture, per testdata/corpus/README.md's
// documented schema.
type corpusRecord struct {
	SessionID              string `json:"session_id"`
	AsOfUnixMS             int64  `json:"as_of_unix_ms"`
	CurrentState           string `json:"current_state"`
	ExpectedState          string `json:"expected_state"`
	CensusPresent          bool   `json:"census_present"`
	LastPromptAtMS         *int64 `json:"last_prompt_at_ms"`
	LastToolAtMS           *int64 `json:"last_tool_at_ms"`
	InstructionsLoadedAtMS *int64 `json:"instructions_loaded_at_ms"`
	ToolCount              int64  `json:"tool_count"`
	TranscriptParseable    bool   `json:"transcript_parseable"`
	SSEClosed              bool   `json:"sse_closed"`
}

// listCorpusFiles returns every *.json path directly under corpusDir, or
// an empty (nil) slice when the directory is absent or has none - both
// treated identically by the caller as "not deposited".
func listCorpusFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		files = append(files, filepath.Join(corpusDir, e.Name()))
	}
	return files
}

// toObservation builds the Observation Advance classifies from one
// corpusRecord.
func (rec corpusRecord) toObservation() sessions.Observation {
	obs := sessions.Observation{SSEClosed: rec.SSEClosed}
	if rec.CensusPresent {
		obs.Census = &census.Snapshot{}
	}
	if rec.CurrentState != "" {
		obs.Domain = &sessions.SessionRecord{
			SessionID:            rec.SessionID,
			State:                rec.CurrentState,
			LastPromptAt:         rec.LastPromptAtMS,
			LastToolAt:           rec.LastToolAtMS,
			InstructionsLoadedAt: rec.InstructionsLoadedAtMS,
			ToolCount:            rec.ToolCount,
		}
	}
	if rec.TranscriptParseable {
		obs.Transcript = &tailer.Record{}
	} else {
		obs.TranscriptErr = cascade.New(cascade.KindInvalidInput, "corpus: labeled unparseable")
	}
	return obs
}

// TestSessionStateAccuracy is the §5.12 accuracy gate: >=0.90 correct
// classifications over the owner-provided corpus. Skips cleanly when no
// corpus has been deposited (see this file's header comment).
func TestSessionStateAccuracy(t *testing.T) {
	files := listCorpusFiles(t)
	if len(files) == 0 {
		t.Skip("sessions: no labeled corpus deposited at testdata/corpus (owner_prereq #5 unmet); skipping §5.12 accuracy gate")
	}

	machine := sessions.NewStateMachine()
	var total, correct int
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("sessions: reading corpus record %s: %v", path, err)
		}
		var rec corpusRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			t.Fatalf("sessions: corpus record %s failed to parse (excluded, not evidence): %v", path, err)
		}
		want, ok := sessions.ParseSessionState(rec.ExpectedState)
		if !ok {
			t.Fatalf("sessions: corpus record %s has unrecognized expected_state %q", path, rec.ExpectedState)
		}
		clk := runtime.NewFixedClock(time.UnixMilli(rec.AsOfUnixMS))
		got, _ := machine.Advance(rec.toObservation(), clk)
		total++
		if got == want {
			correct++
		}
	}

	accuracy := float64(correct) / float64(total)
	if accuracy < 0.90 {
		t.Fatalf("sessions: accuracy %.4f (%d/%d) below the 0.90 floor (06-FORGE-SPEC §5.12)", accuracy, correct, total)
	}
	t.Logf("sessions: accuracy %.4f (%d/%d)", accuracy, correct, total)
}
