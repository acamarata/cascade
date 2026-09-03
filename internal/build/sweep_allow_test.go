package build

import (
	"path/filepath"
	"testing"
)

// TestParseAllowPatterns_SeparatesDenyFromAllow pins that a "!" line never
// becomes a denial and a plain line never becomes an exemption. Getting this
// backwards would turn the pattern that guards an identifier into the rule
// that permits it.
func TestParseAllowPatterns_SeparatesDenyFromAllow(t *testing.T) {
	src := "# comment\nsecret-token\n!^Copyright \\(c\\) [0-9]{4} Someone$\n\n"
	deny, err := ParsePatterns(src)
	if err != nil {
		t.Fatalf("ParsePatterns: %v", err)
	}
	if len(deny) != 1 || deny[0].String() != "secret-token" {
		t.Fatalf("deny set must hold only the plain line, got %v", deny)
	}
	allow, err := ParseAllowPatterns(src)
	if err != nil {
		t.Fatalf("ParseAllowPatterns: %v", err)
	}
	if len(allow) != 1 {
		t.Fatalf("allow set must hold only the ! line, got %v", allow)
	}
}

// TestParseAllowPatterns_NoAllowsIsNotAnError records the asymmetry with
// ParsePatterns on purpose. Zero denials means the gate is unarmed and must
// fail closed. Zero exemptions means the gate is at its strictest, which is
// the state we want to be easiest to reach.
func TestParseAllowPatterns_NoAllowsIsNotAnError(t *testing.T) {
	allow, err := ParseAllowPatterns("secret-token\n")
	if err != nil {
		t.Fatalf("no allow patterns must not be an error: %v", err)
	}
	if len(allow) != 0 {
		t.Fatalf("expected zero allow patterns, got %d", len(allow))
	}
}

// TestParseAllowPatterns_EmptyAllowFailsClosed stops a bare "!" from
// compiling to an empty regexp, which matches every line and would silently
// exempt the entire tree.
func TestParseAllowPatterns_EmptyAllowFailsClosed(t *testing.T) {
	if _, err := ParseAllowPatterns("!\n"); err == nil {
		t.Fatal("a bare ! must be rejected; an empty regexp matches everything")
	}
}

// TestFilterAllowed_ExemptsOnlyTheMatchingLine is the property that made an
// allow pattern preferable to skipping the file: other violations in the
// same file must survive.
func TestFilterAllowed_ExemptsOnlyTheMatchingLine(t *testing.T) {
	allows, err := ParseAllowPatterns("!^Copyright \\(c\\) 2026 Someone$")
	if err != nil {
		t.Fatalf("ParseAllowPatterns: %v", err)
	}
	in := []SweepViolation{
		{Source: "LICENSE", Line: 3, Snippet: "Copyright (c) 2026 Someone"},
		{Source: "LICENSE", Line: 9, Snippet: "contact ops at 10.0.0.1 for keys"},
	}
	kept, dropped := FilterAllowed(in, allows)
	if dropped != 1 {
		t.Fatalf("expected exactly 1 exemption, got %d", dropped)
	}
	if len(kept) != 1 || kept[0].Line != 9 {
		t.Fatalf("the non-exempt violation in the same file must survive, got %v", kept)
	}
}

// TestFilterAllowed_NoAllowsIsIdentity guards the common path: with no
// exemptions configured, nothing may be dropped.
func TestFilterAllowed_NoAllowsIsIdentity(t *testing.T) {
	in := []SweepViolation{{Source: "a", Line: 1, Snippet: "hit"}}
	kept, dropped := FilterAllowed(in, nil)
	if dropped != 0 || len(kept) != 1 {
		t.Fatalf("no allow patterns must drop nothing, got kept=%v dropped=%d", kept, dropped)
	}
}

// TestLoadAllowPatterns_PrefersFileOverEnv mirrors
// TestLoadPatterns_PrefersFileOverEnv: the file-pointer source wins when
// both are set, covering LoadAllowPatterns' file branch and its call into
// ParseAllowPatterns on real file content.
func TestLoadAllowPatterns_PrefersFileOverEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allow.txt")
	writeFileT(t, path, "!^FAKE-FILE-ALLOW$\n")
	t.Setenv(IdentifierPatternsFileEnvVar, path)
	t.Setenv(IdentifierPatternsEnvVar, "!^FAKE-ENV-ALLOW$\n")

	allow, err := LoadAllowPatterns()
	if err != nil {
		t.Fatalf("LoadAllowPatterns: %v", err)
	}
	if len(allow) != 1 || allow[0].String() != "^FAKE-FILE-ALLOW$" {
		t.Fatalf("expected exactly the file-sourced allow pattern, got %v", allow)
	}
}

// TestLoadAllowPatterns_FileReadError covers the ReadFile-error branch of
// the file-pointer case: the env var points at a path with nothing there.
func TestLoadAllowPatterns_FileReadError(t *testing.T) {
	t.Setenv(IdentifierPatternsFileEnvVar, filepath.Join(t.TempDir(), "missing-allow.txt"))
	t.Setenv(IdentifierPatternsEnvVar, "")
	if _, err := LoadAllowPatterns(); err == nil {
		t.Fatal("expected fail-closed error for a missing allow pattern file")
	}
}

// TestLoadAllowPatterns_InlineEnv covers the inline (non-file) env var
// branch on its own, with the file pointer unset.
func TestLoadAllowPatterns_InlineEnv(t *testing.T) {
	t.Setenv(IdentifierPatternsFileEnvVar, "")
	t.Setenv(IdentifierPatternsEnvVar, "!^FAKE-INLINE-ALLOW$\n")

	allow, err := LoadAllowPatterns()
	if err != nil {
		t.Fatalf("LoadAllowPatterns: %v", err)
	}
	if len(allow) != 1 || allow[0].String() != "^FAKE-INLINE-ALLOW$" {
		t.Fatalf("expected exactly the inline allow pattern, got %v", allow)
	}
}

// TestLoadAllowPatterns_FailsClosedWithNoSource covers the neither-
// configured branch: no pattern source at all must be an error, not an
// unarmed (silently-empty) allow list.
func TestLoadAllowPatterns_FailsClosedWithNoSource(t *testing.T) {
	t.Setenv(IdentifierPatternsFileEnvVar, "")
	t.Setenv(IdentifierPatternsEnvVar, "")
	if _, err := LoadAllowPatterns(); err == nil {
		t.Fatal("expected fail-closed error when neither allow-pattern source is configured")
	}
}
