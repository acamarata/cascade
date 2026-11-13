package consult

import (
	"strings"
	"testing"
)

func TestPromptContainsIdentifierRule(t *testing.T) {
	p := FrontRolePrompt()
	rule := "Retain every number, file path, and ID from the source text. Never drop or alter identifiers."
	if !strings.Contains(p, rule) {
		t.Errorf("Prompt does not contain identifier retention rule")
	}
}

func TestPromptForbidsDroppingNumbers(t *testing.T) {
	p := FrontRolePrompt()
	if !strings.Contains(strings.ToLower(p), "number") {
		t.Errorf("Prompt does not mention numbers")
	}
}

func TestPromptForbidsAlteringPaths(t *testing.T) {
	p := FrontRolePrompt()
	if !strings.Contains(strings.ToLower(p), "file path") {
		t.Errorf("Prompt does not mention file paths")
	}
}

func TestPromptMissingRuleFails(t *testing.T) {
	err := ValidatePrompt("you are a rewriter")
	if err == nil {
		t.Fatalf("Expected D4177 error, got nil")
	}
	if err.Error() != "D4177: identifier retention rule missing" {
		t.Errorf("Expected D4177 error, got %v", err)
	}
}
