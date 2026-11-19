package background

import (
	"errors"
	"testing"
)

func TestTriggerOnAgentSubagentKeyword(t *testing.T) {
	req, err := ParseSubSpawnTrigger(InputRegularChat, "Agent/subagent run backup", ToolsPromptComplete)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req == nil {
		t.Fatal("expected request, got nil")
	}
	if req.Action != "sub_spawn" {
		t.Errorf("Action: got %q, want %q", req.Action, "sub_spawn")
	}
	if req.Keyword != "Agent/subagent" {
		t.Errorf("Keyword: got %q, want %q", req.Keyword, "Agent/subagent")
	}
}

func TestHeadPromptAfterToolsPrompt(t *testing.T) {
	req, err := ParseSubSpawnTrigger(InputRegularChat, "Agent/subagent run backup", ToolsPromptComplete)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req == nil {
		t.Fatal("expected request, got nil")
	}
	if req.Prompt != "HeadPrompt" {
		t.Errorf("Prompt: got %q, want %q", req.Prompt, "HeadPrompt")
	}
	if req.After != "ToolsPrompt" {
		t.Errorf("After: got %q, want %q", req.After, "ToolsPrompt")
	}
}

func TestNoTriggerWithoutKeyword(t *testing.T) {
	req, err := ParseSubSpawnTrigger(InputRegularChat, "run backup", ToolsPromptComplete)
	if !errors.Is(err, ErrNoSubSpawnTrigger) {
		t.Errorf("error: got %v, want %v", err, ErrNoSubSpawnTrigger)
	}
	if req != nil {
		t.Errorf("request: got %v, want nil", req)
	}
}

func TestFailsClosedBeforeToolsPromptComplete(t *testing.T) {
	req, err := ParseSubSpawnTrigger(InputRegularChat, "Agent/subagent run backup", ToolsPromptRunning)
	if !errors.Is(err, ErrToolsPromptIncomplete) {
		t.Errorf("error: got %v, want %v", err, ErrToolsPromptIncomplete)
	}
	if req != nil {
		t.Errorf("request: got %v, want nil", req)
	}
}
