package consult

import (
	"errors"
	"testing"
)

// TestPrePromptingErrorFailsClosed verifies that missing or whitespace-only
// context, rule, or prompt yields a zero PostResult and ErrPrePromptingFailed,
// without invoking the generator.
func TestPrePromptingErrorFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		context string
		rule    string
		prompt  string
	}{
		{"empty context", "", "no-secrets", "hello"},
		{"whitespace context", "   ", "no-secrets", "hello"},
		{"empty rule", "user:alice", "", "hello"},
		{"whitespace rule", "user:alice", "\t", "hello"},
		{"empty prompt", "user:alice", "no-secrets", ""},
		{"whitespace prompt", "user:alice", "no-secrets", "  "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			gen := func(s string) (string, error) {
				called = true
				return "", nil
			}

			result, err := RunPromptPhases(tc.context, tc.rule, tc.prompt, gen)

			if called {
				t.Fatal("generator must not be called on pre-prompting failure")
			}
			if result != (PostResult{}) {
				t.Fatalf("expected zero PostResult, got %+v", result)
			}
			if !errors.Is(err, ErrPrePromptingFailed) {
				t.Fatalf("expected ErrPrePromptingFailed, got %v", err)
			}
			if err.Error() != "pre-prompting failed" {
				t.Fatalf("expected error text 'pre-prompting failed', got %q", err.Error())
			}
		})
	}
}

// TestPrePromptingInjectsAllowedContextAndRules verifies that valid inputs
// produce the exact formatted prompt and invoke the generator once.
func TestPrePromptingInjectsAllowedContextAndRules(t *testing.T) {
	var received string
	callCount := 0

	gen := func(s string) (string, error) {
		callCount++
		received = s
		return "output", nil
	}

	result, err := RunPromptPhases("user:alice", "no-secrets", "hello", gen)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected generator called once, got %d", callCount)
	}

	expected := "[user:alice] [no-secrets] hello"
	if received != expected {
		t.Fatalf("expected formatted prompt %q, got %q", expected, received)
	}
	if result.Output != "output" {
		t.Fatalf("expected output 'output', got %q", result.Output)
	}
}
