package log

import (
	"strings"
	"testing"
)

func TestAskBuildsRequestFromPrompt(t *testing.T) {
	var captured Request
	fakeRunner := func(req Request) (string, error) {
		captured = req
		return "ok", nil
	}

	result, err := AskBuildsRequestFromPrompt("hello", fakeRunner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Fatalf("expected 'ok', got %q", result)
	}
	if captured.Prompt != "hello" {
		t.Fatalf("expected prompt 'hello', got %q", captured.Prompt)
	}
}

func TestAskEmptyPromptFails(t *testing.T) {
	called := false
	fakeRunner := func(_ Request) (string, error) {
		called = true
		return "", nil
	}

	_, err := AskBuildsRequestFromPrompt("", fakeRunner)
	if err == nil {
		t.Fatal("expected error for empty prompt")
	}
	if called {
		t.Fatal("runner should not be called for empty prompt")
	}
}

func TestCodexSpecUsesPrivateHome(t *testing.T) {
	privateDir := t.TempDir()
	spec := CodexSpec(privateDir, nil)

	found := false
	for _, env := range spec.Env {
		if strings.HasPrefix(env, "CODEX_HOME=") {
			val := strings.TrimPrefix(env, "CODEX_HOME=")
			if val != privateDir {
				t.Fatalf("expected CODEX_HOME=%s, got CODEX_HOME=%s", privateDir, val)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("CODEX_HOME not found in spec env")
	}
}

func TestCodexSpecIgnoresCallerHome(t *testing.T) {
	privateDir := t.TempDir()
	callerEnv := []string{"CODEX_HOME=/other"}
	spec := CodexSpec(privateDir, callerEnv)

	for _, env := range spec.Env {
		if strings.HasPrefix(env, "CODEX_HOME=") {
			val := strings.TrimPrefix(env, "CODEX_HOME=")
			if val != privateDir {
				t.Fatalf("expected CODEX_HOME=%s, got CODEX_HOME=%s", privateDir, val)
			}
			return
		}
	}
	t.Fatal("CODEX_HOME not found in spec env")
}

func TestCodexRunsViaInjectedRunner(t *testing.T) {
	privateDir := t.TempDir()
	var captured Spec
	fakeRunner := func(s Spec) (int, error) {
		captured = s
		return 0, nil
	}

	exitCode, err := CodexRunsViaInjectedRunner(privateDir, nil, fakeRunner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if len(captured.Args) == 0 {
		t.Fatal("expected args to be set")
	}
	found := false
	for _, env := range captured.Env {
		if strings.HasPrefix(env, "CODEX_HOME=") {
			val := strings.TrimPrefix(env, "CODEX_HOME=")
			if val != privateDir {
				t.Fatalf("expected CODEX_HOME=%s, got CODEX_HOME=%s", privateDir, val)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("CODEX_HOME not found in runner spec env")
	}
}
