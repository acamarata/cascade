package testkit

import (
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

// Purpose: proves, by running the repo's real committed .golangci.yml
//   through the real golangci-lint binary (never a re-typed approximation
//   of the config, never a custom AST scanner), that the two forbidigo
//   rules this ticket adds actually fire on a violation and do NOT
//   false-positive on legitimate uses. Split out of clock_test.go under
//   R-14.117 (15-T0-RULINGS-R14.md) to keep clock_test.go under Art.10.3's
//   300-line cap — this content is about the lint wall this ticket edits,
//   not about Clock's own behavior.
// Constraints: golangci-lint's default package discovery permanently skips
//   any path with a "testdata" component (confirmed by probe: pointing it
//   directly at a testdata/... package yields "no go files to analyze",
//   not "0 issues" — the skip happens at discovery, before config
//   exclusions are even consulted). So the seeded-violation fixtures under
//   testdata/ can only be proven by copying their source into a disposable
//   temp module OUTSIDE any testdata directory (Art.7.1: under
//   t.TempDir()) and running golangci-lint there. Skips (not fails) if
//   golangci-lint is not on PATH, since the "lint" CI job installs it and
//   the "build-test"/"coverage" jobs (this test's other runners) do not.
// SPORT: build/testkit (ADD, placeholder per T-4 sport_updates).

// repoGolangciConfig walks up from this file to find the repo's own
// .golangci.yml, the exact config under review — not a copy retyped by
// hand, which could silently drift from what CI actually runs.
func repoGolangciConfig(t *testing.T) string {
	t.Helper()
	_, file, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("lint probe: runtime.Caller(0) failed; cannot locate .golangci.yml")
	}
	dir := filepath.Dir(file)
	for {
		p := filepath.Join(dir, ".golangci.yml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("lint probe: no .golangci.yml found walking up from %s", file)
		}
		dir = parent
	}
}

// runForbidigoProbe materializes relDir/goFile with src inside a disposable
// temp module (its own go.mod, plus a verbatim copy of the repo's real
// .golangci.yml) and runs `golangci-lint run --enable-only forbidigo`
// there. It returns the combined output and the run's error (nil = 0
// issues found).
func runForbidigoProbe(t *testing.T, relDir, goFile, src string) (string, error) {
	t.Helper()
	bin, err := exec.LookPath("golangci-lint")
	if err != nil {
		t.Skip("lint probe: golangci-lint not on PATH — skipping (the lint CI job installs and runs it)")
	}

	root := t.TempDir()
	pkgDir := filepath.Join(root, relDir)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("lint probe: mkdir %s: %v", pkgDir, err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, goFile), []byte(src), 0o644); err != nil {
		t.Fatalf("lint probe: write fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module lintprobe\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatalf("lint probe: write go.mod: %v", err)
	}
	cfg, err := os.ReadFile(repoGolangciConfig(t))
	if err != nil {
		t.Fatalf("lint probe: reading repo .golangci.yml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".golangci.yml"), cfg, 0o644); err != nil {
		t.Fatalf("lint probe: write .golangci.yml: %v", err)
	}

	// --allow-parallel-runners: this working tree has other tickets'
	// agents concurrently running their own golangci-lint invocations
	// (real lock contention observed, not a theoretical concern) — each
	// probe still targets its own disposable temp module, so a shared
	// process-wide lock is unrelated to what this test is actually
	// verifying and would only produce a false failure.
	cmd := exec.Command(bin, "run", "--allow-parallel-runners", "--enable-only", "forbidigo", "./...")
	cmd.Dir = root
	out, runErr := cmd.CombinedOutput()
	return string(out), runErr
}

func readFixture(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", rel))
	if err != nil {
		t.Fatalf("lint probe: reading fixture %s: %v", rel, err)
	}
	return string(b)
}

// TestLintWall_SeededViolation_NoBareTimeNow proves the no-bare-time.Now
// rule fails a violating file (AC: "seeded-violation fixture red").
func TestLintWall_SeededViolation_NoBareTimeNow(t *testing.T) {
	src := readFixture(t, "seeded-violations/badtime/violation.go")
	out, err := runForbidigoProbe(t, "pkgundertest", "violation.go", src)
	if err == nil {
		t.Fatalf("lint probe: expected forbidigo to reject bare time.Now/time.Since, got a clean run:\n%s", out)
	}
	if !strings.Contains(out, "forbidigo") {
		t.Fatalf("lint probe: expected a forbidigo finding, got:\n%s", out)
	}
}

// TestLintWall_SeededViolation_NoUnseededRand proves the no-unseeded-rand
// rule fails a violating file (AC: "seeded-violation fixture red").
func TestLintWall_SeededViolation_NoUnseededRand(t *testing.T) {
	src := readFixture(t, "seeded-violations/unseededrand/violation.go")
	out, err := runForbidigoProbe(t, "pkgundertest", "violation.go", src)
	if err == nil {
		t.Fatalf("lint probe: expected forbidigo to reject unseeded global math/rand use, got a clean run:\n%s", out)
	}
	if !strings.Contains(out, "forbidigo") {
		t.Fatalf("lint probe: expected a forbidigo finding, got:\n%s", out)
	}
}

// TestLintWall_LegitimateUse_ClockImplementationExempt proves the rule does
// NOT fire on the clock implementation itself, at the exact path
// .golangci.yml's exclusion names (AC: "real tree green").
func TestLintWall_LegitimateUse_ClockImplementationExempt(t *testing.T) {
	src := readFixture(t, "legitimate/clockimpl/clock.go")
	out, err := runForbidigoProbe(t, filepath.Join("internal", "runtime"), "clock.go", src)
	if err != nil {
		t.Fatalf("lint probe: expected internal/runtime/clock.go to be exempt from forbidigo, got a failure:\n%s", out)
	}
}

// TestLintWall_LegitimateUse_TestFileExempt proves neither rule fires
// inside a _test.go file.
func TestLintWall_LegitimateUse_TestFileExempt(t *testing.T) {
	src := readFixture(t, "legitimate/testfile/something_test.go")
	out, err := runForbidigoProbe(t, "pkgundertest", "something_test.go", src)
	if err != nil {
		t.Fatalf("lint probe: expected _test.go bare time.Now/rand use to be exempt, got a failure:\n%s", out)
	}
}

// TestLintWall_LegitimateUse_SeededRandExempt proves properly seeded,
// injected randomness (a *rand.Rand method call, not the package-level
// global source) never matches the no-unseeded-rand pattern.
func TestLintWall_LegitimateUse_SeededRandExempt(t *testing.T) {
	src := readFixture(t, "legitimate/seededrand/seeded.go")
	out, err := runForbidigoProbe(t, "pkgundertest", "seeded.go", src)
	if err != nil {
		t.Fatalf("lint probe: expected seeded *rand.Rand use to be exempt, got a failure:\n%s", out)
	}
}
