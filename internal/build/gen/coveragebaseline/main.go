// Package main is a LOCAL/CI generator for
// internal/build/testdata/coverage-baseline.json (AUD-040): the coverage
// ratchet baseline, filled mechanically from a measured coverage profile
// rather than hand-typed. Run via
//
//	go run ./internal/build/gen/coveragebaseline --profile <coverage.out> --add-missing
//
// to add an entry for every profiled, floor-bearing package that has none
// yet (never touching an existing entry — see internal/build's
// AddMissingBaselineEntries doc), or with --check in place of
// --add-missing to fail (exit 1) without writing anything when the
// committed baseline is out of date against profile.
//
// This binary deliberately never shells out (no os/exec): it reads the
// profile file the caller names with --profile and the repo's own go.mod
// to find its root, and does nothing else — the repo's own dev-environment
// notes (R-14.114) record that `go` is rtk-wrapped and can silently drop
// flags or misreport exit codes for anything that shells out to it, a risk
// this generator has no reason to take on for a task that is pure file
// I/O plus computation already covered by internal/build's own package
// tests.
//
// Inputs: --profile (a go test -coverprofile file), the repo's go.mod (to
// find its root, via runtime.Caller — no subprocess),
// internal/build/testdata/coverage-baseline.json.
// Outputs (--add-missing only): coverage-baseline.json, rewritten in
// place.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"

	"github.com/acamarata/cascade/internal/build"
	"github.com/acamarata/cascade/internal/output"
)

// coverageModulePath is this module's import path — a profile's package
// keys carry it as a prefix and must be stripped before matching
// coverage-baseline.json's module-relative keys. Mirrors
// internal/build/coveragegate_test.go's coverageModulePath constant
// (duplicated deliberately: that one is test-only, this is production
// code in a different package, and neither should import the other's
// test-only symbol).
const coverageModulePath = "github.com/acamarata/cascade"

func main() {
	os.Exit(realMain(os.Args[1:], output.NewDefault(false, false, false, false)))
}

// realMain is main()'s entire body as a testable, exit-code-returning
// function — os.Exit itself is the only thing left out of it, because
// calling os.Exit from inside a test would kill the test binary. Every
// branch (a bad flag, a failed --profile/--add-missing precondition, a
// repoRoot() failure, and everything dispatch covers) is reachable from a
// test through this one function.
func realMain(args []string, w *output.Writer) int {
	// A fresh FlagSet per call (never the global flag.CommandLine, which
	// panics on a second Parse in the same process) — this is what makes
	// realMain itself callable more than once from a table-driven test.
	fs := flag.NewFlagSet("coveragebaseline", flag.ContinueOnError)
	flags, err := parseFlags(fs, args)
	if err != nil {
		w.Fail(err)
		return 2
	}
	if err := validateFlags(flags.profile, flags.addMissing, flags.check); err != nil {
		w.Fail(err)
		return 1
	}
	root, err := repoRoot()
	if err != nil {
		w.Fail(err)
		return 1
	}
	return dispatch(w, root, flags.profile, flags.addMissing, flags.check)
}

// cliFlags is this binary's three flags, parsed.
type cliFlags struct {
	profile           string
	addMissing, check bool
}

// parseFlags registers --profile/--add-missing/--check on fs and parses
// args, so a test can exercise the exact flag definitions main() uses with
// its own throwaway flag.FlagSet — flag.CommandLine can only be parsed
// once per process, which would make this untestable if main() parsed it
// inline.
func parseFlags(fs *flag.FlagSet, args []string) (cliFlags, error) {
	profile := fs.String("profile", "", "path to a `go test -coverprofile` output file (required)")
	addMissing := fs.Bool("add-missing", false,
		"rewrite coverage-baseline.json, adding an entry for every profiled floor-bearing package with none yet")
	check := fs.Bool("check", false,
		"exit 1 (without writing) if --add-missing would add any entry — the baseline is out of date")
	if err := fs.Parse(args); err != nil {
		return cliFlags{}, err
	}
	return cliFlags{profile: *profile, addMissing: *addMissing, check: *check}, nil
}

// dispatch runs the chosen mode against root and reports the outcome via
// w, returning the process exit code. Split out of main() so every
// downstream branch (add-missing success, check pass, check fail, a run()
// error surfacing in either mode) is testable against a fixture root
// (t.TempDir()) — main() itself keeps only flag parsing and repoRoot(),
// the two things a fixture-root test cannot reach.
func dispatch(w *output.Writer, root, profile string, addMissing, check bool) int {
	added, err := run(root, profile, addMissing)
	if err != nil {
		w.Fail(err)
		return 1
	}
	if check {
		if len(added) != 0 {
			w.Fail(fmt.Errorf("coveragebaseline: baseline is out of date, %d package(s) missing an entry: %v", len(added), added))
			return 1
		}
		w.Println("coveragebaseline: check OK, no missing entries")
		return 0
	}
	w.Println("coveragebaseline: added", len(added), "entr(y/ies):", added)
	return 0
}

// validateFlags requires exactly one of --add-missing / --check, and a
// non-empty --profile, before either mode does any file I/O.
func validateFlags(profile string, addMissing, check bool) error {
	if profile == "" {
		return fmt.Errorf("coveragebaseline: --profile is required")
	}
	if addMissing == check {
		return fmt.Errorf("coveragebaseline: pass exactly one of --add-missing or --check")
	}
	return nil
}

// run loads profile and the committed baseline, computes the missing
// entries, and — only when addMissing is true — writes the updated
// baseline back to disk. It returns the added package keys either way, so
// --check can fail on a non-empty result without writing.
func run(root, profilePath string, addMissing bool) ([]string, error) {
	profile, err := loadProfile(profilePath)
	if err != nil {
		return nil, err
	}

	baselinePath := filepath.Join(root, "internal", "build", "testdata", "coverage-baseline.json")
	existingData, err := os.ReadFile(baselinePath)
	if err != nil {
		return nil, fmt.Errorf("coveragebaseline: reading baseline %s: %w", baselinePath, err)
	}
	existing, err := build.ParseBaseline(existingData)
	if err != nil {
		return nil, fmt.Errorf("coveragebaseline: parsing baseline %s: %w", baselinePath, err)
	}

	roots, err := build.MainPackages(root)
	if err != nil {
		return nil, fmt.Errorf("coveragebaseline: discovering composition roots: %w", err)
	}

	updated, added := build.AddMissingBaselineEntries(existing, profile, roots)
	if addMissing {
		if err := os.WriteFile(baselinePath, build.FormatBaselineJSON(updated), 0o644); err != nil { //nolint:gosec // generated artifact, not a secret
			return nil, fmt.Errorf("coveragebaseline: writing %s: %w", baselinePath, err)
		}
	}
	return added, nil
}

// loadProfile reads and parses profilePath, stripping the module prefix
// so its keys match coverage-baseline.json's module-relative form.
func loadProfile(profilePath string) (map[string]*build.CoverageStats, error) {
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return nil, fmt.Errorf("coveragebaseline: reading profile %s: %w", profilePath, err)
	}
	raw, err := build.ParseCoverageProfile(data)
	if err != nil {
		return nil, fmt.Errorf("coveragebaseline: parsing profile %s: %w", profilePath, err)
	}
	stripped := make(map[string]*build.CoverageStats, len(raw))
	for k, v := range raw {
		stripped[build.StripModulePrefix(k, coverageModulePath)] = v
	}
	return stripped, nil
}

// repoRoot resolves the checkout root by walking up from this source
// file's own location (via runtime.Caller) until a go.mod is found — no
// subprocess, see this file's package doc.
func repoRoot() (string, error) {
	_, file, _, ok := goruntime.Caller(0)
	if !ok {
		return "", fmt.Errorf("coveragebaseline: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("coveragebaseline: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}
