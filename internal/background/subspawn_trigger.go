// Package background provides sub-spawn trigger logic for M2222.
package background

import (
	"errors"
	"strings"
)

// InputKind represents the kind of input being processed.
type InputKind string

// InputRegularChat is the input kind for regular chat messages.
const InputRegularChat InputKind = "regular_chat"

// ToolsPromptState represents the state of the tools prompt.
type ToolsPromptState string

// ToolsPromptComplete indicates the tools prompt has finished.
const ToolsPromptComplete ToolsPromptState = "complete"

// ToolsPromptRunning indicates the tools prompt is still running.
const ToolsPromptRunning ToolsPromptState = "running"

// ErrNoSubSpawnTrigger is returned when the keyword is absent or input is not regular chat.
var ErrNoSubSpawnTrigger = errors.New("no sub_spawn trigger")

// ErrToolsPromptIncomplete is returned when the tools prompt is not yet complete.
var ErrToolsPromptIncomplete = errors.New("tools prompt incomplete")

// ParseSubSpawnTrigger parses regular chat input and returns a sub_spawn request
// when the exact keyword Agent/subagent is present and ToolsPrompt is complete.
// It returns an error if the keyword is absent, input is not regular chat,
// or ToolsPrompt is incomplete.
func ParseSubSpawnTrigger(kind InputKind, text string, state ToolsPromptState) (*SubSpawnRequest, error) {
	if kind != InputRegularChat {
		return nil, ErrNoSubSpawnTrigger
	}

	if !strings.Contains(text, "Agent/subagent") {
		return nil, ErrNoSubSpawnTrigger
	}

	if state != ToolsPromptComplete {
		return nil, ErrToolsPromptIncomplete
	}

	return &SubSpawnRequest{
		Action:  "sub_spawn",
		Keyword: "Agent/subagent",
		Prompt:  "HeadPrompt",
		After:   "ToolsPrompt",
	}, nil
}
