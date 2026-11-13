package mod

import (
	"errors"
	"strings"
	"testing"
)

func TestLanePathSkillsAssetsNative(t *testing.T) {
	got := LanePath("skills-assets")
	if got != "skills-assets" {
		t.Fatalf("LanePath(\"skills-assets\") = %q, want \"skills-assets\"", got)
	}
}

func TestLanePathNoGithubAIPrefix(t *testing.T) {
	got := LanePath("skills-assets")
	if strings.HasPrefix(got, ".github/ai/") {
		t.Fatalf("LanePath(\"skills-assets\") = %q, should not start with \".github/ai/\"", got)
	}
}

func TestCheckNoGithubAIPrefixRejects(t *testing.T) {
	err := CheckNoGithubAIPrefix([]string{".github/ai/skills-assets/a.md"})
	if !errors.Is(err, ErrLegacyPrefix) {
		t.Fatalf("CheckNoGithubAIPrefix(...) = %v, want ErrLegacyPrefix", err)
	}
}

func TestCheckNoGithubAIPrefixAccepts(t *testing.T) {
	err := CheckNoGithubAIPrefix([]string{"skills-assets/a.md"})
	if err != nil {
		t.Fatalf("CheckNoGithubAIPrefix(...) = %v, want nil", err)
	}
}
