package fmx

import (
	"errors"
	"reflect"
	"testing"
)

func TestLoopLaunchPlainCommand(t *testing.T) {
	input := []string{"loop", "run"}
	spec := LaunchSpec{
		Command:      input,
		OuterSandbox: false,
	}

	got, err := BuildLoopLaunch(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"loop", "run"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// Verify returned slice does not alias input
	input[0] = "mutated"
	if got[0] == "mutated" {
		t.Error("BuildLoopLaunch aliased the caller slice")
	}

	// Verify duplicate ordinary args preserved
	specDup := LaunchSpec{
		Command:      []string{"loop", "run", "--flag", "--flag"},
		OuterSandbox: false,
	}
	gotDup, err := BuildLoopLaunch(specDup)
	if err != nil {
		t.Fatalf("unexpected error for duplicates: %v", err)
	}
	wantDup := []string{"loop", "run", "--flag", "--flag"}
	if !reflect.DeepEqual(gotDup, wantDup) {
		t.Errorf("duplicate args: got %v, want %v", gotDup, wantDup)
	}

	// Fails closed on empty command
	_, errEmpty := BuildLoopLaunch(LaunchSpec{})
	if !errors.Is(errEmpty, ErrInvalidLoopCommand) {
		t.Errorf("expected ErrInvalidLoopCommand for zero-value spec, got %v", errEmpty)
	}

	_, errNil := BuildLoopLaunch(LaunchSpec{Command: nil})
	if !errors.Is(errNil, ErrInvalidLoopCommand) {
		t.Errorf("expected ErrInvalidLoopCommand for nil command, got %v", errNil)
	}

	_, errEmptySlice := BuildLoopLaunch(LaunchSpec{Command: []string{}})
	if !errors.Is(errEmptySlice, ErrInvalidLoopCommand) {
		t.Errorf("expected ErrInvalidLoopCommand for empty slice command, got %v", errEmptySlice)
	}
}
