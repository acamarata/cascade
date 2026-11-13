package hj

import (
	"errors"
	"testing"
)

func TestControllerJobRecordRetainsMaskedPromptCommand(t *testing.T) {
	testMaskedPromptCommand(t)
	testInvalidPromptCommands(t)
}

func testMaskedPromptCommand(t *testing.T) {
	rec, err := RecordControllerJob("run --prompt MASKED")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if rec.Command != "run --prompt MASKED" {
		t.Errorf("expected command 'run --prompt MASKED', got %q", rec.Command)
	}
	if rec.ID != "1" {
		t.Errorf("expected ID '1', got %q", rec.ID)
	}

	records := ControllerRecords()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Command != "run --prompt MASKED" || records[0].ID != "1" {
		t.Errorf("snapshot mismatch: %+v", records[0])
	}

	rec2, err := RecordControllerJob("run --prompt secret")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if rec2.ID != "2" {
		t.Errorf("expected ID '2', got %q", rec2.ID)
	}
	if rec2.Command != "run --prompt *****" {
		t.Errorf("expected masked command 'run --prompt *****', got %q", rec2.Command)
	}

	records = ControllerRecords()
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	records[0].Command = "mutated"

	recordsAgain := ControllerRecords()
	if recordsAgain[0].Command != "run --prompt MASKED" {
		t.Errorf("storage was mutated by snapshot")
	}
}

func testInvalidPromptCommands(t *testing.T) {
	invalids := []string{
		"",
		"   ",
		"run",
		"run --prompt",
		"run --prompt --flag",
		"run --prompt MASKED --prompt MASKED",
		"run '--prompt' MASKED",
		"run \"--prompt\" MASKED",
		"run \\--prompt MASKED",
	}
	for _, cmd := range invalids {
		_, err := RecordControllerJob(cmd)
		if !errors.Is(err, ErrInvalidPromptCommand) {
			t.Errorf("expected ErrInvalidPromptCommand for %q, got %v", cmd, err)
		}
	}
}
