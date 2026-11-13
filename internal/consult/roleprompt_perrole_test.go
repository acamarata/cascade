package consult

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestRolePromptPerRoleDiffers verifies that LoadRolePrompt yields distinct
// results for Critic and Judge roles on repeated reads with no cache/state,
// that missing files yield exact distinct built-ins, and that no home
// expansion occurs.
func TestRolePromptPerRoleDiffers(t *testing.T) {
	dir := t.TempDir()

	criticBody := "You are the critic reviewer."
	judgeBody := "You are the judge evaluator."

	if err := os.WriteFile(filepath.Join(dir, "Critic.md"), []byte(criticBody), 0o644); err != nil {
		t.Fatalf("write Critic.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Judge.md"), []byte(judgeBody), 0o644); err != nil {
		t.Fatalf("write Judge.md: %v", err)
	}

	// Distinct bodies yield distinct results on repeated reads.
	c1 := LoadRolePrompt(dir, "Critic")
	j1 := LoadRolePrompt(dir, "Judge")
	c2 := LoadRolePrompt(dir, "Critic")
	j2 := LoadRolePrompt(dir, "Judge")

	if c1 == j1 {
		t.Error("Critic and Judge returned identical prompts; expected distinct results")
	}
	if c1 != c2 {
		t.Error("repeated Critic reads differ; expected stable results without cache")
	}
	if j1 != j2 {
		t.Error("repeated Judge reads differ; expected stable results without cache")
	}

	// Constraint: no more than 5000 Unicode code points.
	for _, name := range []string{"Critic", "Judge"} {
		prompt := LoadRolePrompt(dir, name)
		if utf8.RuneCountInString(prompt) > 5000 {
			t.Fatalf("STOP: role %s prompt exceeds 5000 Unicode code points (%d)", name, utf8.RuneCountInString(prompt))
		}
	}

	// Missing files yield exact distinct built-ins.
	emptyDir := t.TempDir()
	mc := LoadRolePrompt(emptyDir, "Critic")
	mj := LoadRolePrompt(emptyDir, "Judge")

	if mc == mj {
		t.Error("missing Critic and Judge built-ins are identical; expected distinct fallbacks")
	}
	if mc == "" || mj == "" {
		t.Error("built-in fallback must not be empty")
	}

	// No home expansion occurs.
	if strings.Contains(mc, "~") && strings.HasPrefix(mc, "~") {
		t.Error("home expansion detected in Critic built-in")
	}
	if strings.Contains(mj, "~") && strings.HasPrefix(mj, "~") {
		t.Error("home expansion detected in Judge built-in")
	}

	// Role containing path separators falls back to the built-in.
	slash := LoadRolePrompt(dir, "../Critic")
	expectedSlash := LoadRolePrompt(emptyDir, "../Critic")
	if slash != expectedSlash {
		t.Errorf("role with path separator did not fall back to built-in; got %q, want %q", slash, expectedSlash)
	}
	if slash == criticBody {
		t.Error("role with path separator read file from dir; expected built-in fallback")
	}
}
