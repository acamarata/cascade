package consult

import (
	"strings"
	"testing"
)

func TestSubChatPromptRootPath(t *testing.T) {
	prompt, err := SubChatPrompt("/a/b", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(prompt, "/a/b") {
		t.Errorf("prompt does not contain root path: %s", prompt)
	}
}

func TestSubChatPromptInheritsContexts(t *testing.T) {
	prompt, err := SubChatPrompt("/x", []string{"c1", "c2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(prompt, "c1") {
		t.Errorf("prompt does not contain c1: %s", prompt)
	}
	if !strings.Contains(prompt, "c2") {
		t.Errorf("prompt does not contain c2: %s", prompt)
	}
}

func TestSubChatPromptOutermostFirst(t *testing.T) {
	prompt, err := SubChatPrompt("/x", []string{"outer", "inner"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	outerIdx := strings.Index(prompt, "outer")
	innerIdx := strings.Index(prompt, "inner")
	if outerIdx == -1 || innerIdx == -1 {
		t.Fatalf("missing outer or inner in prompt: %s", prompt)
	}
	if outerIdx >= innerIdx {
		t.Errorf("outer does not appear before inner in prompt: %s", prompt)
	}
}

func TestSubChatPromptEmptyContextFails(t *testing.T) {
	_, err := SubChatPrompt("", nil)
	if err == nil {
		t.Fatal("expected error for empty root path")
	}
	if !strings.Contains(err.Error(), "D6129") {
		t.Errorf("error does not contain D6129: %v", err)
	}
}
