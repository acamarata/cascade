package main

// Purpose: run() and validateFlags() against a synthetic fixture root
// (t.TempDir()), never this repo's own coverage-baseline.json — this
// package's own coverage floor (internal/build/gen/coveragebaseline is
// TierCore, 85%) is proven here, independent of internal/build's own
// package tests for AddMissingBaselineEntries/FormatBaselineJSON.

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/output"
)

// writeFixtureRoot builds a minimal module root: go.mod (so repoRoot's
// go.mod walk would find it, though these tests call run() directly with
// an explicit root) plus internal/build/testdata/coverage-baseline.json.
func writeFixtureRoot(t *testing.T, baselineJSON string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/acamarata/cascade\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("writing go.mod: %v", err)
	}
	testdataDir := filepath.Join(root, "internal", "build", "testdata")
	if err := os.MkdirAll(testdataDir, 0o755); err != nil {
		t.Fatalf("MkdirAll testdata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(testdataDir, "coverage-baseline.json"), []byte(baselineJSON), 0o644); err != nil {
		t.Fatalf("writing coverage-baseline.json: %v", err)
	}
	return root
}

func writeProfile(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "profile.out")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing profile: %v", err)
	}
	return path
}

const fixtureBaseline = `{
  "internal/build": { "tier": "core", "floor": 85.0, "baseline": 90.0 }
}
`

const fixtureProfile = `mode: atomic
github.com/acamarata/cascade/internal/policy/policy.go:1.1,3.2 5 1
`

// TestRun_AddMissingWritesOnlyMissingEntries proves the end-to-end
// --add-missing path: an existing internal/build entry survives
// untouched, and internal/policy (fully covered, absent from the fixture
// baseline) is added.
func TestRun_AddMissingWritesOnlyMissingEntries(t *testing.T) {
	root := writeFixtureRoot(t, fixtureBaseline)
	profilePath := writeProfile(t, root, fixtureProfile)

	added, err := run(root, profilePath, true)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(added) != 1 || added[0] != "internal/policy" {
		t.Fatalf("added = %v, want [internal/policy]", added)
	}

	got, err := os.ReadFile(filepath.Join(root, "internal", "build", "testdata", "coverage-baseline.json"))
	if err != nil {
		t.Fatalf("reading rewritten baseline: %v", err)
	}
	if !strings.Contains(string(got), `"internal/build": { "tier": "core", "floor": 85.0, "baseline": 90.0 }`) {
		t.Errorf("existing internal/build entry not preserved verbatim, got:\n%s", got)
	}
	if !strings.Contains(string(got), `"internal/policy": { "tier": "security", "floor": 90.0, "baseline": 100.0 }`) {
		t.Errorf("internal/policy entry not added as expected, got:\n%s", got)
	}
}

// TestRun_CheckModeNeverWrites proves --check (addMissing=false) computes
// the same "added" answer but never touches the file on disk.
func TestRun_CheckModeNeverWrites(t *testing.T) {
	root := writeFixtureRoot(t, fixtureBaseline)
	profilePath := writeProfile(t, root, fixtureProfile)
	baselinePath := filepath.Join(root, "internal", "build", "testdata", "coverage-baseline.json")
	before, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatalf("reading baseline: %v", err)
	}

	added, err := run(root, profilePath, false)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(added) != 1 || added[0] != "internal/policy" {
		t.Fatalf("added = %v, want [internal/policy]", added)
	}
	after, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatalf("reading baseline after check: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("--check mode rewrote the baseline file: before=%q after=%q", before, after)
	}
}

// TestRun_NothingMissingIsIdempotent proves a profile with no new package
// leaves added empty and the file byte-identical after --add-missing.
func TestRun_NothingMissingIsIdempotent(t *testing.T) {
	root := writeFixtureRoot(t, fixtureBaseline)
	profilePath := writeProfile(t, root, "mode: atomic\ngithub.com/acamarata/cascade/internal/build/gate.go:1.1,3.2 90 1\n")
	baselinePath := filepath.Join(root, "internal", "build", "testdata", "coverage-baseline.json")
	before, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatalf("reading baseline: %v", err)
	}

	added, err := run(root, profilePath, true)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("added = %v, want none", added)
	}
	after, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatalf("reading baseline after add-missing: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("baseline changed with nothing missing: before=%q after=%q", before, after)
	}
}

// TestValidateFlags covers the CLI's own precondition checks.
func TestValidateFlags(t *testing.T) {
	cases := []struct {
		name              string
		profile           string
		addMissing, check bool
		wantErrContains   string
	}{
		{"missing profile", "", true, false, "--profile is required"},
		{"neither flag", "p", false, false, "exactly one"},
		{"both flags", "p", true, true, "exactly one"},
		{"add-missing ok", "p", true, false, ""},
		{"check ok", "p", false, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateFlags(c.profile, c.addMissing, c.check)
			if c.wantErrContains == "" {
				if err != nil {
					t.Fatalf("validateFlags: unexpected error %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErrContains) {
				t.Fatalf("validateFlags: got %v, want error containing %q", err, c.wantErrContains)
			}
		})
	}
}

// TestRepoRoot proves repoRoot finds this checkout's own go.mod by
// walking up from its source file, with no subprocess.
func TestRepoRoot(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repoRoot returned %s, no go.mod there: %v", root, err)
	}
}

// TestLoadProfile_BadPathErrors proves a missing profile file is a clear
// error, not a panic.
func TestLoadProfile_BadPathErrors(t *testing.T) {
	if _, err := loadProfile(filepath.Join(t.TempDir(), "does-not-exist.out")); err == nil {
		t.Fatal("loadProfile: expected an error for a missing file")
	}
}

// TestDispatch covers every branch main() delegates to dispatch(): a
// run() error, --add-missing success, --check success (nothing missing)
// and --check failure (something missing) — all against a fixture root,
// never the real checkout.
func TestDispatch(t *testing.T) {
	t.Run("run error surfaces and exits 1", func(t *testing.T) {
		root := writeFixtureRoot(t, fixtureBaseline)
		w := testOutputWriter()
		if code := dispatch(w, root, filepath.Join(root, "no-such.out"), true, false); code != 1 {
			t.Fatalf("dispatch = %d, want 1", code)
		}
	})
	t.Run("add-missing success exits 0", func(t *testing.T) {
		root := writeFixtureRoot(t, fixtureBaseline)
		profilePath := writeProfile(t, root, fixtureProfile)
		w := testOutputWriter()
		if code := dispatch(w, root, profilePath, true, false); code != 0 {
			t.Fatalf("dispatch = %d, want 0", code)
		}
	})
	t.Run("check with nothing missing exits 0", func(t *testing.T) {
		root := writeFixtureRoot(t, fixtureBaseline)
		profilePath := writeProfile(t, root, "mode: atomic\ngithub.com/acamarata/cascade/internal/build/gate.go:1.1,3.2 90 1\n")
		w := testOutputWriter()
		if code := dispatch(w, root, profilePath, false, true); code != 0 {
			t.Fatalf("dispatch = %d, want 0", code)
		}
	})
	t.Run("check with something missing exits 1", func(t *testing.T) {
		root := writeFixtureRoot(t, fixtureBaseline)
		profilePath := writeProfile(t, root, fixtureProfile)
		w := testOutputWriter()
		if code := dispatch(w, root, profilePath, false, true); code != 1 {
			t.Fatalf("dispatch = %d, want 1", code)
		}
	})
}

func testOutputWriter() *output.Writer {
	return output.NewDefault(false, true, false, true)
}

// TestParseFlags proves the three flags main() defines round-trip through
// a throwaway FlagSet (the shape a test can safely reuse per subtest,
// unlike the global flag.CommandLine main() itself parses).
func TestParseFlags(t *testing.T) {
	got, err := parseFlags(flag.NewFlagSet("test", flag.ContinueOnError),
		[]string{"--profile", "cover.out", "--add-missing"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := cliFlags{profile: "cover.out", addMissing: true, check: false}
	if got != want {
		t.Fatalf("parseFlags = %+v, want %+v", got, want)
	}
}

// TestParseFlags_UnknownFlagErrors proves a bad flag is a parse error, not
// a panic or a silently ignored argument.
func TestParseFlags_UnknownFlagErrors(t *testing.T) {
	if _, err := parseFlags(flag.NewFlagSet("test", flag.ContinueOnError), []string{"--nope"}); err == nil {
		t.Fatal("parseFlags: expected an error for an unknown flag")
	}
}

// TestRealMain covers every branch of main()'s body (a bad flag, a failed
// precondition, and the repoRoot()-succeeds/dispatch-fails path — the one
// realMain branch a fixture root cannot isolate, since repoRoot() always
// resolves THIS checkout): realMain never touches the real
// coverage-baseline.json in any of these cases, because each one fails
// before dispatch's run() would ever write it (an unknown flag and a
// missing --profile fail in parseFlags/validateFlags; a real --profile
// pointed at a nonexistent file fails inside run()'s loadProfile, before
// any write).
func TestRealMain(t *testing.T) {
	w := testOutputWriter()
	if code := realMain([]string{"--nope"}, w); code != 2 {
		t.Errorf("realMain(bad flag) = %d, want 2", code)
	}
	if code := realMain([]string{}, w); code != 1 {
		t.Errorf("realMain(no --profile) = %d, want 1", code)
	}
	if code := realMain([]string{"--profile", "/no/such/file", "--add-missing"}, w); code != 1 {
		t.Errorf("realMain(missing profile file) = %d, want 1", code)
	}
}
