package consult

import (
	"errors"
	"testing"
)

func TestPostPromptingReturnsCleanupSummaryExtras(t *testing.T) {
	gen := func(s string) (string, error) {
		return " draft ", nil
	}
	res, err := RunPromptPhases("ctx", "rule", "prompt", gen)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Output != "draft" {
		t.Errorf("Output = %q, want %q", res.Output, "draft")
	}
	if res.Summary != "cleanup done" {
		t.Errorf("Summary = %q, want %q", res.Summary, "cleanup done")
	}
	if res.Extras != "post-complete" {
		t.Errorf("Extras = %q, want %q", res.Extras, "post-complete")
	}

	// Empty generated output is accepted
	genEmpty := func(s string) (string, error) {
		return "", nil
	}
	resEmpty, err := RunPromptPhases("ctx", "rule", "prompt", genEmpty)
	if err != nil {
		t.Fatalf("unexpected error on empty output: %v", err)
	}
	if resEmpty.Output != "" {
		t.Errorf("Output = %q, want empty", resEmpty.Output)
	}
	if resEmpty.Summary != "cleanup done" {
		t.Errorf("Summary = %q, want %q", resEmpty.Summary, "cleanup done")
	}
	if resEmpty.Extras != "post-complete" {
		t.Errorf("Extras = %q, want %q", resEmpty.Extras, "post-complete")
	}
}

func TestPhasesExecuteAroundMainGeneration(t *testing.T) {
	calls := 0
	var receivedInput string
	gen := func(s string) (string, error) {
		calls++
		receivedInput = s
		return "result", nil
	}

	res, err := RunPromptPhases("ctx", "rule", "prompt", gen)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Errorf("generator called %d times, want 1", calls)
	}
	if receivedInput != "[ctx] [rule] prompt" {
		t.Errorf("generator input = %q, want %q", receivedInput, "[ctx] [rule] prompt")
	}
	if res.Output != "result" {
		t.Errorf("Output = %q, want %q", res.Output, "result")
	}
	if res.Summary != "cleanup done" {
		t.Errorf("Summary = %q, want %q", res.Summary, "cleanup done")
	}
	if res.Extras != "post-complete" {
		t.Errorf("Extras = %q, want %q", res.Extras, "post-complete")
	}

	// main error identity propagates with zero result
	mainErr := errors.New("main phase failed")
	genErr := func(s string) (string, error) {
		return "", mainErr
	}
	resErr, err := RunPromptPhases("ctx", "rule", "prompt", genErr)
	if !errors.Is(err, mainErr) {
		t.Errorf("err = %v, want %v", err, mainErr)
	}
	if resErr.Output != "" || resErr.Summary != "" || resErr.Extras != "" {
		t.Errorf("resErr = %+v, want zero result", resErr)
	}

	// nil generator yields errors.Is ErrInvalidGenerator
	resNil, err := RunPromptPhases("ctx", "rule", "prompt", nil)
	if !errors.Is(err, ErrInvalidGenerator) {
		t.Errorf("err = %v, want %v", err, ErrInvalidGenerator)
	}
	if resNil.Output != "" || resNil.Summary != "" || resNil.Extras != "" {
		t.Errorf("resNil = %+v, want zero result", resNil)
	}
}
