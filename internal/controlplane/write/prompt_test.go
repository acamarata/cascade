package write

import (
	"strings"
	"testing"
)

func TestInjectPromptElements(t *testing.T) {
	prompt, err := InjectPrompt("2026-11-06", "stop-at-3")
	if err != nil {
		t.Fatalf("InjectPrompt returned error: %v", err)
	}

	wants := []string{
		"2026-11-06",
		"stop-at-3",
		"never invent sources",
	}
	for _, want := range wants {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q: %q", want, prompt)
		}
	}
}

func TestInjectPromptDate(t *testing.T) {
	prompt, err := InjectPrompt("2026-11-06", "stop-at-3")
	if err != nil {
		t.Fatalf("InjectPrompt returned error: %v", err)
	}

	if !strings.Contains(prompt, "2026-11-06") {
		t.Errorf("prompt missing date: %q", prompt)
	}
}

func TestInjectPromptFinishingRule(t *testing.T) {
	prompt, err := InjectPrompt("2026-11-06", "stop-at-3")
	if err != nil {
		t.Fatalf("InjectPrompt returned error: %v", err)
	}

	if !strings.Contains(prompt, "stop-at-3") {
		t.Errorf("prompt missing finishing rule: %q", prompt)
	}
}

func TestInjectPromptNeverInventSources(t *testing.T) {
	prompt, err := InjectPrompt("2026-11-06", "stop-at-3")
	if err != nil {
		t.Fatalf("InjectPrompt returned error: %v", err)
	}

	if !strings.Contains(prompt, "never invent sources") {
		t.Errorf("prompt missing source limit: %q", prompt)
	}
}

func TestInjectPromptEmptyFinishingRuleFails(t *testing.T) {
	prompt, err := InjectPrompt("2026-11-06", "")
	if err == nil {
		t.Fatal("InjectPrompt with empty finishing rule must return an error")
	}

	if !strings.Contains(err.Error(), "D2729") {
		t.Errorf("error missing D2729: %v", err)
	}

	if prompt != "" {
		t.Errorf("prompt must be empty on error, got %q", prompt)
	}
}
