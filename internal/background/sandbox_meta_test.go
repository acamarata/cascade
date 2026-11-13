package background

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestHookMetadataLinePerSpawn verifies that AgentLogHook returns the correct
// spawn metadata as compact JSON plus a trailing newline, and that JSON encoding
// correctly escapes embedded newlines in the agent name.
func TestHookMetadataLinePerSpawn(t *testing.T) {
	e := SpawnEvent{Agent: "worker", Prompt: "hello"}
	lint := func() ([]string, error) { return []string{}, nil }
	_, meta, err := AgentLogHook(e, lint)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"kind":"spawn","agent":"worker"}` + "\n"
	if meta != want {
		t.Fatalf("metadata = %q, want %q", meta, want)
	}

	// Ensure JSON encoding escapes embedded newlines.
	e2 := SpawnEvent{Agent: "wo\nrker", Prompt: "hello"}
	_, meta2, err2 := AgentLogHook(e2, lint)
	if err2 != nil {
		t.Fatalf("unexpected error: %v", err2)
	}
	if !strings.Contains(meta2, `\n`) {
		t.Fatalf("expected newline to be escaped in metadata, got %q", meta2)
	}
	var m struct {
		Kind  string `json:"kind"`
		Agent string `json:"agent"`
	}
	if err := json.Unmarshal([]byte(meta2), &m); err != nil {
		t.Fatalf("metadata is not valid JSON: %v", err)
	}
	if m.Agent != "wo\nrker" {
		t.Fatalf("decoded agent = %q, want %q", m.Agent, "wo\nrker")
	}
}

// TestHookNoPromptLeakage verifies that changing a secret Prompt never changes
// either returned string or callback arguments, and that callback error text
// is suppressed behind ErrSpawnLint.
func TestHookNoPromptLeakage(t *testing.T) {
	lint := func() ([]string, error) { return nil, nil }

	e1 := SpawnEvent{Agent: "worker", Prompt: "secret-1"}
	lintJSON1, meta1, err1 := AgentLogHook(e1, lint)
	if err1 != nil {
		t.Fatalf("unexpected error: %v", err1)
	}

	e2 := SpawnEvent{Agent: "worker", Prompt: "secret-2-completely-different"}
	lintJSON2, meta2, err2 := AgentLogHook(e2, lint)
	if err2 != nil {
		t.Fatalf("unexpected error: %v", err2)
	}

	if lintJSON1 != lintJSON2 {
		t.Fatalf("lint JSON changed when prompt changed: %q vs %q", lintJSON1, lintJSON2)
	}
	if meta1 != meta2 {
		t.Fatalf("metadata changed when prompt changed: %q vs %q", meta1, meta2)
	}

	// Callback error text is suppressed behind ErrSpawnLint.
	errLint := func() ([]string, error) { return nil, errors.New("leaky secret error text") }
	_, _, err3 := AgentLogHook(e1, errLint)
	if err3 == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err3, ErrSpawnLint) {
		t.Fatalf("expected ErrSpawnLint, got %v", err3)
	}
	if strings.Contains(err3.Error(), "leaky") {
		t.Fatalf("callback error text leaked: %v", err3)
	}
}
