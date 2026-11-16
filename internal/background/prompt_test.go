package background

import "testing"

// TestSystemPromptContainsAskCwInstruction verifies that the system prompt
// contains the ask_webchat instruction when given a default system prompt.
func TestSystemPromptContainsAskCwInstruction(t *testing.T) {
	cfg := Config{Base: "You are a helpful assistant."}
	result := SystemPrompt(cfg)
	if !containsAskWebchat(result) {
		t.Errorf("SystemPrompt() = %q, want it to contain ask_webchat instruction", result)
	}
}

// TestSystemPromptNotEmpty verifies that an empty config still produces
// a non-empty system prompt with the ask_webchat instruction.
func TestSystemPromptNotEmpty(t *testing.T) {
	cfg := Config{}
	result := SystemPrompt(cfg)
	if result == "" {
		t.Error("SystemPrompt() returned empty string, want non-empty")
	}
	if !containsAskWebchat(result) {
		t.Errorf("SystemPrompt() = %q, want it to contain ask_webchat instruction", result)
	}
}

func containsAskWebchat(s string) bool {
	return len(s) > 0 && (s[len(s)-len(AskCwInstruction):] == AskCwInstruction ||
		findSubstring(s, AskCwInstruction))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
