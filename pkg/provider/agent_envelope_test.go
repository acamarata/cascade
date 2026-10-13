// Purpose: tests for SpawnEnvelope.Check (P1-AGT-02): one refusing case per
//
//	rule, each asserted by identity of the wrapped sentinel and by the
//	field named in the message; the path-segment boundary; the passing case.
//
// SPORT: pkg.provider.AgentProvider tests (EXTEND) - P1-AGT-02.
package provider_test

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

var envelopeNow = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

// completeEnvelope returns a passing envelope and the worktree it covers,
// both under one temp base so the paths are absolute on every OS.
func completeEnvelope(t *testing.T) (provider.SpawnEnvelope, string, string) {
	t.Helper()
	base := filepath.ToSlash(t.TempDir())
	env := provider.SpawnEnvelope{
		Digest:      strings.Repeat("ab", 32),
		Principal:   "principal-1",
		Roots:       []string{base + "/a/b"},
		Tools:       []string{"Read"},
		Sensitivity: provider.SensitivityInternal,
		ExpiresAt:   envelopeNow.Add(time.Hour),
	}
	return env, base, base + "/a/b/work"
}

func TestSpawnEnvelopeCheckRefusesIncomplete(t *testing.T) {
	cases := []struct {
		name  string
		field string
		edit  func(base string, e *provider.SpawnEnvelope, worktree *string)
	}{
		{"short digest", "Digest", func(_ string, e *provider.SpawnEnvelope, _ *string) { e.Digest = "abc" }},
		{"upper-case digest", "Digest", func(_ string, e *provider.SpawnEnvelope, _ *string) { e.Digest = strings.Repeat("AB", 32) }},
		{"non-hex digest", "Digest", func(_ string, e *provider.SpawnEnvelope, _ *string) { e.Digest = strings.Repeat("zz", 32) }},
		{"empty digest", "Digest", func(_ string, e *provider.SpawnEnvelope, _ *string) { e.Digest = "" }},
		{"empty principal", "Principal", func(_ string, e *provider.SpawnEnvelope, _ *string) { e.Principal = "" }},
		{"nil roots", "Roots is empty", func(_ string, e *provider.SpawnEnvelope, _ *string) { e.Roots = nil }},
		{"relative root", "Roots", func(_ string, e *provider.SpawnEnvelope, _ *string) { e.Roots = []string{"rel/dir"} }},
		{"unclean root", "Roots", func(b string, e *provider.SpawnEnvelope, _ *string) { e.Roots = []string{b + "/a/x/../b"} }},
		{"trailing slash root", "Roots", func(b string, e *provider.SpawnEnvelope, _ *string) { e.Roots = []string{b + "/a/b/"} }},
		{"one bad root among good", "Roots", func(b string, e *provider.SpawnEnvelope, _ *string) { e.Roots = []string{b + "/a/b", "rel"} }},
		{"zero expiry", "ExpiresAt", func(_ string, e *provider.SpawnEnvelope, _ *string) { e.ExpiresAt = time.Time{} }},
		{"expiry equal to now", "ExpiresAt", func(_ string, e *provider.SpawnEnvelope, _ *string) { e.ExpiresAt = envelopeNow }},
		{"past expiry", "ExpiresAt", func(_ string, e *provider.SpawnEnvelope, _ *string) { e.ExpiresAt = envelopeNow.Add(-time.Second) }},
		{"worktree outside roots", "worktree", func(b string, _ *provider.SpawnEnvelope, w *string) { *w = b + "/elsewhere" }},
		{"sibling-prefix worktree", "worktree", func(b string, _ *provider.SpawnEnvelope, w *string) { *w = b + "/a/bc" }},
		{"empty worktree", "worktree", func(_ string, _ *provider.SpawnEnvelope, w *string) { *w = "" }},
		{"relative worktree", "worktree", func(_ string, _ *provider.SpawnEnvelope, w *string) { *w = "a/b/work" }},
		{"unclean worktree", "worktree", func(b string, _ *provider.SpawnEnvelope, w *string) { *w = b + "/a/b/../b/work" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env, base, worktree := completeEnvelope(t)
			c.edit(base, &env, &worktree)
			err := env.Check(worktree, envelopeNow)
			if err == nil {
				t.Fatalf("Check passed an envelope broken in %s", c.field)
			}
			if errors.Unwrap(err) != provider.ErrEnvelopeIncomplete {
				t.Fatalf("Check error %v does not wrap ErrEnvelopeIncomplete by identity", err)
			}
			if !strings.Contains(err.Error(), c.field) {
				t.Fatalf("Check error %q does not name %q", err.Error(), c.field)
			}
		})
	}
	env, _, worktree := completeEnvelope(t)
	if err := env.Check(worktree, envelopeNow); err != nil {
		t.Fatalf("a complete envelope refused: %v", err)
	}
}

func TestSpawnEnvelopeWorktreeUnderRoot(t *testing.T) {
	env, base, _ := completeEnvelope(t)
	env.Roots = []string{base + "/other", base + "/a/b"}
	for name, wt := range map[string]string{
		"equal to a root":       base + "/a/b",
		"under a root":          base + "/a/b/c/d",
		"under the second root": base + "/other/x",
	} {
		if err := env.Check(wt, envelopeNow); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for name, wt := range map[string]string{
		"parent of a root":      base + "/a",
		"sibling with prefix":   base + "/a/bc",
		"sibling with prefix 2": base + "/a/b-work",
	} {
		if err := env.Check(wt, envelopeNow); err == nil {
			t.Fatalf("%s: Check passed worktree %s", name, wt)
		}
	}
}

// On Windows an envelope root is spelled "/C:/dir" (pkg/envelope requires a
// leading slash); Check must accept that spelling and nothing broader.
func TestSpawnEnvelopeWindowsRootSpelling(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the /C:/dir root spelling only exists on Windows")
	}
	env, base, worktree := completeEnvelope(t)
	env.Roots = []string{"/" + base + "/a/b"}
	if err := env.Check(worktree, envelopeNow); err != nil {
		t.Fatalf("envelope spelling of a Windows root refused: %v", err)
	}
	if err := env.Check(base+"/a/bc", envelopeNow); err == nil {
		t.Fatal("a sibling-prefix worktree passed under the envelope spelling")
	}
}
