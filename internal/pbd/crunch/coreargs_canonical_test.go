package crunch

import (
	"reflect"
	"testing"
)

// TestExtractCoreArgsMatchesRunOnce validates the canonical fixture and edge cases.
func TestExtractCoreArgsMatchesRunOnce(t *testing.T) {
	t.Run("canonical fixture", func(t *testing.T) {
		fixture := []string{
			"--dir", "/work",
			"--verify=go test ./...",
			"--prompt", "fix bug",
			"--scope=a.go",
			"--scope", "b.go",
		}
		want := CoreArgs{
			Dir:    "/work",
			Verify: "go test ./...",
			Prompt: "fix bug",
			Scope:  []string{"a.go", "b.go"},
		}
		got, err := ExtractCoreArgs(fixture)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("both forms work", func(t *testing.T) {
		args := []string{"--dir=/tmp", "--verify", "true"}
		got, err := ExtractCoreArgs(args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Dir != "/tmp" || got.Verify != "true" {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("defaults", func(t *testing.T) {
		args := []string{"--dir=/tmp", "--prompt=hi"}
		got, err := ExtractCoreArgs(args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Verify != "" {
			t.Errorf("expected empty Verify, got %q", got.Verify)
		}
		if got.Scope != nil {
			t.Errorf("expected nil Scope, got %v", got.Scope)
		}
	})

	t.Run("input slice unchanged", func(t *testing.T) {
		fixture := []string{"--dir", "/work", "--verify=go test ./...", "--prompt", "fix bug", "--scope=a.go", "--scope", "b.go"}
		orig := make([]string, len(fixture))
		copy(orig, fixture)
		_, _ = ExtractCoreArgs(fixture)
		if !reflect.DeepEqual(fixture, orig) {
			t.Errorf("input slice was modified: got %v, want %v", fixture, orig)
		}
	})
}
