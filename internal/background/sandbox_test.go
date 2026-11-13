package background

import (
	"errors"
	"testing"
)

func TestHookAdvisoryLintJSON(t *testing.T) {
	t.Run("violations list", func(t *testing.T) {
		e := SpawnEvent{Agent: "assistant", Prompt: "do something"}
		lint := func() ([]string, error) {
			return []string{"model_set"}, nil
		}
		got, meta, err := AgentLogHook(e, lint)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "{\"advisory\":true,\"violations\":[\"model_set\"]}\n"
		if got != want {
			t.Errorf("lintJSON = %q, want %q", got, want)
		}
		if meta != "" {
			t.Errorf("metadata = %q, want empty", meta)
		}
	})

	t.Run("nil violations encodes empty array", func(t *testing.T) {
		e := SpawnEvent{Agent: "assistant", Prompt: "do something"}
		lint := func() ([]string, error) {
			return nil, nil
		}
		got, meta, err := AgentLogHook(e, lint)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "{\"advisory\":true,\"violations\":[]}\n"
		if got != want {
			t.Errorf("lintJSON = %q, want %q", got, want)
		}
		if meta != "" {
			t.Errorf("metadata = %q, want empty", meta)
		}
	})

	t.Run("empty violations encodes empty array", func(t *testing.T) {
		e := SpawnEvent{Agent: "assistant", Prompt: "do something"}
		lint := func() ([]string, error) {
			return []string{}, nil
		}
		got, _, err := AgentLogHook(e, lint)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "{\"advisory\":true,\"violations\":[]}\n"
		if got != want {
			t.Errorf("lintJSON = %q, want %q", got, want)
		}
	})

	t.Run("nil lint closure returns ErrInvalidSpawn", func(t *testing.T) {
		e := SpawnEvent{Agent: "assistant", Prompt: "do something"}
		got, meta, err := AgentLogHook(e, nil)
		if !errors.Is(err, ErrInvalidSpawn) {
			t.Errorf("err = %v, want ErrInvalidSpawn", err)
		}
		if got != "" {
			t.Errorf("lintJSON = %q, want empty", got)
		}
		if meta != "" {
			t.Errorf("metadata = %q, want empty", meta)
		}
	})

	t.Run("blank Agent returns ErrInvalidSpawn", func(t *testing.T) {
		e := SpawnEvent{Agent: "  ", Prompt: "do something"}
		lint := func() ([]string, error) {
			return []string{"model_set"}, nil
		}
		got, meta, err := AgentLogHook(e, lint)
		if !errors.Is(err, ErrInvalidSpawn) {
			t.Errorf("err = %v, want ErrInvalidSpawn", err)
		}
		if got != "" {
			t.Errorf("lintJSON = %q, want empty", got)
		}
		if meta != "" {
			t.Errorf("metadata = %q, want empty", meta)
		}
	})

	t.Run("empty Agent returns ErrInvalidSpawn", func(t *testing.T) {
		e := SpawnEvent{Agent: "", Prompt: "do something"}
		lint := func() ([]string, error) {
			return []string{"model_set"}, nil
		}
		got, meta, err := AgentLogHook(e, lint)
		if !errors.Is(err, ErrInvalidSpawn) {
			t.Errorf("err = %v, want ErrInvalidSpawn", err)
		}
		if got != "" {
			t.Errorf("lintJSON = %q, want empty", got)
		}
		if meta != "" {
			t.Errorf("metadata = %q, want empty", meta)
		}
	})

	t.Run("lint error returns ErrSpawnLint", func(t *testing.T) {
		e := SpawnEvent{Agent: "assistant", Prompt: "do something"}
		lint := func() ([]string, error) {
			return nil, errors.New("lint exploded")
		}
		got, meta, err := AgentLogHook(e, lint)
		if !errors.Is(err, ErrSpawnLint) {
			t.Errorf("err = %v, want ErrSpawnLint", err)
		}
		if got != "" {
			t.Errorf("lintJSON = %q, want empty", got)
		}
		if meta != "" {
			t.Errorf("metadata = %q, want empty", meta)
		}
	})

	t.Run("prompt is never inspected", func(t *testing.T) {
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
	})
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
