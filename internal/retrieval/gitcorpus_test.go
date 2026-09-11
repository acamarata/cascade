// Package retrieval (this file) tests the code-corpus type: the real-git
// counterpart requirement (Art.2), the registered-kind refusal (Art.5's
// fail-closed rule), and the file-enumeration/chunker error paths.
//
// SPORT: internal.retrieval.IngestGitRepo/ADDED test coverage,
//
//	internal.retrieval.GitTrackedFiles/ADDED test coverage
//	(P1-E25-W5-S52-T6).
package retrieval

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
)

// runGit runs a git subcommand in dir, failing the test on error. It is
// the test's own driver for the real git CLI — never a hand-authored
// dialect standing in for git's actual behavior.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// newRealGitRepo builds a real repository at t.TempDir() via the actual
// git CLI: init, write source files, add, commit. This is the Art.2
// real-counterpart fixture — no synthetic git-index dialect.
func newRealGitRepo(t *testing.T) string {
	t.Helper()
	if v, err := exec.Command("git", "--version").CombinedOutput(); err == nil {
		t.Logf("provenance: %s", strings.TrimSpace(string(v)))
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	files := map[string]string{
		"main.go":      "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n",
		"util/util.go": "package util\n\n// Add sums two ints.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n",
		".gitignore":   "ignored.go\n",
	}
	for name, content := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	// An untracked, gitignored file: proves enumeration reads the git
	// INDEX, never the filesystem directly.
	if err := os.WriteFile(filepath.Join(dir, "ignored.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("writing ignored.go: %v", err)
	}
	runGit(t, dir, "add", "main.go", "util/util.go", ".gitignore")
	runGit(t, dir, "commit", "-q", "-m", "initial commit")
	return dir
}

// TestCodeCorpus_RealGitCounterpart is the ticket's Art.2 requirement: a
// real git repository, indexed through the real ingestor, must return the
// committed content with correct path provenance and must never surface
// the gitignored, untracked file.
func TestCodeCorpus_RealGitCounterpart(t *testing.T) {
	repo := newRealGitRepo(t)

	c, chunks, err := IngestGitRepo(context.Background(), repo, "project/cascade", corpus.TrustTrusted)
	if err != nil {
		t.Fatalf("IngestGitRepo: %v", err)
	}
	if c.ID != corpus.CorpusIDCode {
		t.Errorf("Corpus.ID = %q, want %q", c.ID, corpus.CorpusIDCode)
	}
	if c.Trust != corpus.TrustTrusted {
		t.Errorf("Corpus.Trust = %q, want %q", c.Trust, corpus.TrustTrusted)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("returned Corpus fails Validate: %v", err)
	}

	paths := map[string]bool{}
	var sawAdd bool
	for _, ch := range chunks {
		paths[ch.Path] = true
		if strings.Contains(string(ch.Content), "func Add") {
			sawAdd = true
		}
		if ch.Path == "ignored.go" {
			t.Fatal("gitignored, untracked file was ingested; enumeration must read the git index, not the filesystem")
		}
	}
	if !paths["main.go"] || !paths["util/util.go"] {
		t.Fatalf("missing expected tracked paths, got %v", paths)
	}
	if !sawAdd {
		t.Fatal("committed function body \"func Add\" not found in any returned chunk")
	}
}

// TestCodeCorpus_ScopeRegistered asserts corpus.CorpusIDCode is a
// registered kind, and that an unrecognized kind is refused rather than
// treated as an empty result.
func TestCodeCorpus_ScopeRegistered(t *testing.T) {
	if err := corpus.ValidateCorpusKind(corpus.CorpusIDCode); err != nil {
		t.Fatalf("ValidateCorpusKind(%q): %v", corpus.CorpusIDCode, err)
	}
	err := corpus.ValidateCorpusKind("not-a-real-kind")
	if err == nil {
		t.Fatal("expected a refusal for an unregistered corpus kind, got nil")
	}
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("want KindInvalidInput, got kind=%v ok=%v err=%v", kind, ok, err)
	}
}

// TestIngestGitRepo_EnumerationFailure exercises the file-enumeration
// error path: a path that is not a git repository at all.
func TestIngestGitRepo_EnumerationFailure(t *testing.T) {
	dir := t.TempDir() // never git-initialized
	_, _, err := IngestGitRepo(context.Background(), dir, "project/cascade", corpus.TrustTrusted)
	if err == nil {
		t.Fatal("expected an error ingesting a non-git directory, got nil")
	}
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindUnavailable {
		t.Fatalf("want KindUnavailable, got kind=%v ok=%v err=%v", kind, ok, err)
	}
}

// TestIngestGitRepo_ChunkerError exercises the chunker-error path: a
// tracked ".go" file whose content fails to parse as Go source.
func TestIngestGitRepo_ChunkerError(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "broken.go"), []byte("package ((( not go"), 0o644); err != nil {
		t.Fatalf("writing broken.go: %v", err)
	}
	runGit(t, dir, "add", "broken.go")
	runGit(t, dir, "commit", "-q", "-m", "broken")

	_, _, err := IngestGitRepo(context.Background(), dir, "project/cascade", corpus.TrustTrusted)
	if err == nil {
		t.Fatal("expected a chunker error for unparseable Go source, got nil")
	}
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("want KindInvalidInput, got kind=%v ok=%v err=%v", kind, ok, err)
	}
}

// TestIngestGitRepo_UntrustedPropagates asserts the trust tier a caller
// passes in is threaded through unchanged, never silently upgraded.
func TestIngestGitRepo_UntrustedPropagates(t *testing.T) {
	repo := newRealGitRepo(t)
	c, _, err := IngestGitRepo(context.Background(), repo, "project/cascade", corpus.TrustUntrustedSource)
	if err != nil {
		t.Fatalf("IngestGitRepo: %v", err)
	}
	if c.Trust != corpus.TrustUntrustedSource {
		t.Fatalf("Corpus.Trust = %q, want %q (untrusted must propagate, not be upgraded)", c.Trust, corpus.TrustUntrustedSource)
	}
}

// TestIngestGitRepo_InvalidScopeRefused asserts an invalid scope
// reference refuses via Corpus.Validate rather than producing a
// partially-classified Corpus.
func TestIngestGitRepo_InvalidScopeRefused(t *testing.T) {
	repo := newRealGitRepo(t)
	_, _, err := IngestGitRepo(context.Background(), repo, "", corpus.TrustTrusted)
	if err == nil {
		t.Fatal("expected a refusal for an empty scope reference, got nil")
	}
}

// TestGitTrackedFiles_OversizeFileRefused asserts a file over the
// per-file byte bound refuses rather than being silently truncated.
func TestGitTrackedFiles_OversizeFileRefused(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	big := make([]byte, maxCodeFileBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), big, 0o644); err != nil {
		t.Fatalf("writing big.txt: %v", err)
	}
	runGit(t, dir, "add", "big.txt")
	runGit(t, dir, "commit", "-q", "-m", "big")

	_, _, err := IngestGitRepo(context.Background(), dir, "project/cascade", corpus.TrustTrusted)
	if err == nil {
		t.Fatal("expected a refusal for an oversize tracked file, got nil")
	}
}
