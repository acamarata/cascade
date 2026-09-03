// Tests for sweep.go (P1-E01-W1-S01-T5). Pure unit tests for pattern
// parsing/loading and content scanning; TestIdentifierSweepGate_Live is the
// opt-in live gate. The heavy real-git pre-push hook integration test lives
// in the sibling hook_test.go (R-14.117 split, to keep this file under
// Art.10.3's 300-line cap).
package build

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

// sweepModuleRoot locates the repo root by walking up from this file.
// Shared with hook_test.go.
func sweepModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("sweep gate: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("sweep gate: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

func TestParsePatterns(t *testing.T) {
	pats, err := ParsePatterns("# a comment\n\nFAKE-ONE\n  FAKE-TWO-[0-9]+  \n")
	if err != nil {
		t.Fatalf("ParsePatterns: %v", err)
	}
	if len(pats) != 2 {
		t.Fatalf("expected 2 patterns (blank/comment skipped), got %d: %v", len(pats), pats)
	}

	if _, err := ParsePatterns("("); err == nil {
		t.Fatal("expected an error for an invalid regex pattern")
	}
	if _, err := ParsePatterns("\n# only comments\n\n"); err == nil {
		t.Fatal("expected fail-closed error for a source with zero usable patterns")
	}
	if _, err := ParsePatterns(""); err == nil {
		t.Fatal("expected fail-closed error for empty input")
	}
}

func TestLoadPatternsFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patterns.txt")
	writeFileT(t, path, "FAKE-ONE\n")
	pats, err := LoadPatternsFromFile(path)
	if err != nil || len(pats) != 1 {
		t.Fatalf("LoadPatternsFromFile: got %v, %v", pats, err)
	}

	if _, err := LoadPatternsFromFile(filepath.Join(dir, "missing.txt")); err == nil {
		t.Fatal("expected fail-closed error for a missing pattern file")
	}
}

func TestLoadPatternsFromEnv(t *testing.T) {
	t.Setenv("CASCADE_TEST_PATTERNS_VAR", "FAKE-ONE\n")
	pats, err := LoadPatternsFromEnv("CASCADE_TEST_PATTERNS_VAR")
	if err != nil || len(pats) != 1 {
		t.Fatalf("LoadPatternsFromEnv: got %v, %v", pats, err)
	}

	if _, err := LoadPatternsFromEnv("CASCADE_TEST_PATTERNS_VAR_UNSET"); err == nil {
		t.Fatal("expected fail-closed error for an unset env var")
	}
}

func TestLoadPatterns_FailsClosedWithNoSource(t *testing.T) {
	t.Setenv(IdentifierPatternsFileEnvVar, "")
	t.Setenv(IdentifierPatternsEnvVar, "")
	if _, err := LoadPatterns(); err == nil {
		t.Fatal("expected fail-closed error when neither pattern source is configured")
	}
}

func TestLoadPatterns_PrefersFileOverEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "patterns.txt")
	writeFileT(t, path, "FAKE-FILE-PATTERN\n")
	t.Setenv(IdentifierPatternsFileEnvVar, path)
	t.Setenv(IdentifierPatternsEnvVar, "FAKE-ENV-PATTERN\n")

	pats, err := LoadPatterns()
	if err != nil {
		t.Fatalf("LoadPatterns: %v", err)
	}
	if len(pats) != 1 || pats[0].String() != "FAKE-FILE-PATTERN" {
		t.Fatalf("expected exactly the file-sourced pattern (file source takes priority over env), got %v", pats)
	}
}

func TestSweepContent(t *testing.T) {
	pats := []*regexp.Regexp{regexp.MustCompile(`FAKE-[A-Z]+`)}
	v := SweepContent(pats, "example.txt", []byte("line one\nhas FAKE-HIT here\nclean line"))
	if len(v) != 1 || v[0].Line != 2 || v[0].Source != "example.txt" {
		t.Fatalf("expected exactly one hit on line 2, got %+v", v)
	}
}

func TestSweepCommitMessages(t *testing.T) {
	pats := []*regexp.Regexp{regexp.MustCompile(`FAKE-HIT`)}
	v := SweepCommitMessages(pats, []string{"feat: clean", "fix: contains FAKE-HIT"})
	if len(v) != 1 || v[0].Source != "commit-message[1]" {
		t.Fatalf("expected exactly one hit labeled commit-message[1], got %+v", v)
	}
}

// TestSweepFiles_SeededFixtures scans the fixtures through SweepContent
// rather than SweepFiles. SweepFiles deliberately skips any path under a
// testdata segment (SweepSkipsPath) — seeded fixtures exist to CONTAIN the
// strings the gate hunts for, so scanning them would make the gate
// permanently red on its own test data and block every push. Reading the
// fixture here and scanning its bytes exercises the same matching logic
// without re-entering the exclusion this test is not testing.
func TestSweepFiles_SeededFixtures(t *testing.T) {
	root := sweepModuleRoot(t)
	scanFixture := func(t *testing.T, rel string, pats []*regexp.Regexp) []SweepViolation {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read fixture %s: %v", rel, err)
		}
		return SweepContent(pats, rel, data)
	}
	fixtureDir := filepath.Join("internal", "build", "testdata", "seeded-violations", "identifiers")
	// Composed at runtime — see the note in hook_test.go.
	pats := []*regexp.Regexp{regexp.MustCompile("SEEDED-" + "IDENTIFIER-" + "LEAK-" + `\d+`)}

	v := scanFixture(t, filepath.Join(fixtureDir, "leak.txt"), pats)
	if len(v) == 0 {
		t.Fatal("expected the seeded leak fixture to trip the marker pattern, found none")
	}

	clean := scanFixture(t, filepath.Join(fixtureDir, "clean.txt"), pats)
	if len(clean) != 0 {
		t.Fatalf("expected the clean fixture to produce zero hits, got %+v", clean)
	}

	// Same fixture, the home-path and free-mail shapes it also seeds:
	// proves a pattern set the sweep gate could plausibly be configured
	// with (this repo's real pattern source is external, never tracked —
	// see sweep.go's package doc) catches structural shapes too, not just
	// exact-marker patterns.
	shapePats := []*regexp.Regexp{
		regexp.MustCompile(`/Users/[A-Za-z0-9_.-]+`),
		regexp.MustCompile(`[A-Za-z0-9._%+-]+@gmail\.com`),
	}
	shapes := scanFixture(t, filepath.Join(fixtureDir, "leak.txt"), shapePats)
	if len(shapes) < 2 {
		t.Fatalf("expected >=2 shape-pattern hits on the leak fixture (home path + gmail), got %d: %+v", len(shapes), shapes)
	}
}

func TestSweepFiles_MissingFileFailsClosed(t *testing.T) {
	root := sweepModuleRoot(t)
	pats := []*regexp.Regexp{regexp.MustCompile(`anything`)}
	if _, err := SweepFiles(pats, root, []string{"does/not/exist.txt"}); err == nil {
		t.Fatal("expected an error for a missing file, not a silent skip")
	}
}

func TestListTrackedFiles_RealGit(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "fixture@example.invalid")
	runGit(t, dir, "config", "user.name", "Fixture")
	writeFileT(t, filepath.Join(dir, "tracked.txt"), "tracked\n")
	writeFileT(t, filepath.Join(dir, ".gitignore"), "untracked.txt\n")
	writeFileT(t, filepath.Join(dir, "untracked.txt"), "should not appear\n")
	runGit(t, dir, "add", "tracked.txt", ".gitignore")
	runGit(t, dir, "commit", "-q", "-m", "feat: seed tracked files")

	files, err := ListTrackedFiles(dir)
	if err != nil {
		t.Fatalf("ListTrackedFiles: %v", err)
	}
	var sawTracked, sawUntracked bool
	for _, f := range files {
		if f == "tracked.txt" {
			sawTracked = true
		}
		if f == "untracked.txt" {
			sawUntracked = true
		}
	}
	if !sawTracked || sawUntracked {
		t.Fatalf("expected only tracked.txt (never the gitignored untracked.txt), got %v", files)
	}
}

// TestIdentifierSweepGate_Live runs the sweep against the real repo tree
// (and, if a range is given, real commit messages) only when explicitly
// requested — see sweep.go's package doc and commits.go's identical
// rationale for why this must not fire on a bare `go test ./...`.
func TestIdentifierSweepGate_Live(t *testing.T) {
	if os.Getenv("CASCADE_HYGIENE_RUN") != "1" {
		t.Skip("CASCADE_HYGIENE_RUN not set; live identifier sweep not requested")
	}
	patterns, err := LoadPatterns()
	if err != nil {
		t.Fatal(err) // fail closed: unreadable/missing pattern source blocks, never skips
	}
	root := sweepModuleRoot(t)
	files, err := ListTrackedFiles(root)
	if err != nil {
		t.Fatalf("identifier sweep: %v", err)
	}
	violations, err := SweepFiles(patterns, root, files)
	if err != nil {
		t.Fatalf("identifier sweep: %v", err)
	}
	allows, aerr := LoadAllowPatterns()
	if aerr != nil {
		t.Fatal(aerr) // fail closed: an unreadable allow list blocks, never skips
	}
	if rng := os.Getenv("CASCADE_HYGIENE_SWEEP_RANGE"); rng != "" {
		commits, cerr := LoadCommitRange(root, rng)
		if cerr != nil {
			t.Fatalf("identifier sweep: loading commit range %q: %v", rng, cerr)
		}
		msgs := make([]string, len(commits))
		for i, c := range commits {
			msgs[i] = c.Message
		}
		violations = append(violations, SweepCommitMessages(patterns, msgs)...)
	}
	violations, exempted := FilterAllowed(violations, allows)
	// Report the exemption count even on success. An allow list that
	// quietly grows is how a gate stops being a gate, so the number is
	// always visible in the log rather than only when something fails.
	t.Logf("identifier sweep: %d line(s) exempted by allow patterns", exempted)
	if len(violations) > 0 {
		t.Fatalf("identifier sweep: %d violation(s) found:\n%s", len(violations), FormatSweepViolations(violations))
	}
}

// TestSweepSkipsPath pins the exclusion that keeps the gate off its own
// fixtures. It matches whole path segments only: an unanchored "testdata"
// check is how the lint wall came to exempt every path merely containing
// that word, and this gate must not repeat it.
func TestSweepSkipsPath(t *testing.T) {
	skipped := []string{
		"internal/build/testdata/seeded-violations/identifiers/leak.txt",
		"testdata/x.txt",
		"pkg/plugin/testdata/example-pbd.toml",
	}
	kept := []string{
		"internal/build/sweep.go",
		"internal/nottestdatagen/gen.go",
		"docs/testdata-guide.md",
		"pkg/testdataset/x.go",
	}
	for _, p := range skipped {
		if !SweepSkipsPath(p) {
			t.Errorf("SweepSkipsPath(%q) = false, want true", p)
		}
	}
	for _, p := range kept {
		if SweepSkipsPath(p) {
			t.Errorf("SweepSkipsPath(%q) = true, want false (substring, not a path segment)", p)
		}
	}
}

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
