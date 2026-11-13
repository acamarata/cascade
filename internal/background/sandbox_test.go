package background

import (
	"errors"
	"testing"
)

func TestHookAdvisoryLintJSON(t *testing.T) {
	cases := []struct {
		name      string
		violation []string
		want      string
		checkMeta bool
	}{
		{"violations list", []string{"model_set"}, "{\"advisory\":true,\"violations\":[\"model_set\"]}\n", true},
		{"nil violations encodes empty array", nil, "{\"advisory\":true,\"violations\":[]}\n", true},
		{"empty violations encodes empty array", []string{}, "{\"advisory\":true,\"violations\":[]}\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := SpawnEvent{Agent: "assistant", Prompt: "do something"}
			lint := func() ([]string, error) {
				return tc.violation, nil
			}
			got, meta, err := AgentLogHook(e, lint)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("lintJSON = %q, want %q", got, tc.want)
			}
			if tc.checkMeta && meta != "" {
				t.Errorf("metadata = %q, want empty", meta)
			}
		})
	}
}

func TestHookAdvisoryLintJSONErrors(t *testing.T) {
	okLint := func() ([]string, error) {
		return []string{"model_set"}, nil
	}
	cases := []struct {
		name    string
		agent   string
		lint    func() ([]string, error)
		wantErr error
	}{
		{"nil lint closure returns ErrInvalidSpawn", "assistant", nil, ErrInvalidSpawn},
		{"blank Agent returns ErrInvalidSpawn", "  ", okLint, ErrInvalidSpawn},
		{"empty Agent returns ErrInvalidSpawn", "", okLint, ErrInvalidSpawn},
		{"lint error returns ErrSpawnLint", "assistant", func() ([]string, error) {
			return nil, errors.New("lint exploded")
		}, ErrSpawnLint},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := SpawnEvent{Agent: tc.agent, Prompt: "do something"}
			got, meta, err := AgentLogHook(e, tc.lint)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if got != "" {
				t.Errorf("lintJSON = %q, want empty", got)
			}
			if meta != "" {
				t.Errorf("metadata = %q, want empty", meta)
			}
		})
	}
}

func TestHookAdvisoryLintJSONPromptNeverInspected(t *testing.T) {
	// Even with a suspicious prompt, output contains no prompt text.
	e := SpawnEvent{Agent: "assistant", Prompt: "secret-payload-value"}
	lint := func() ([]string, error) {
		return []string{"model_set"}, nil
	}
	got, _, err := AgentLogHook(e, lint)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contains(got, "secret-payload-value") || contains(got, "Prompt") || contains(got, "secret") {
		t.Errorf("lintJSON leaked prompt: %q", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsHelper(s, sub))
}

func containsHelper(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
