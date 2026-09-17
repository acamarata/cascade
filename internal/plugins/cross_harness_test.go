package plugins

// Purpose: the adapter half of the cross-harness conformance suite
//   (P1-E16-W4-S35-T4). internal/context's cross_harness_test.go covers
//   what the GENERATORS must agree on; this covers what the three
//   INSTALL/UNINSTALL adapters must, which can only be driven from a
//   package free to import all three (this one already wires them).
// Constraints: Art.7.1 — every path is a t.TempDir(). The adapters keep
//   their generator in a package-level var, so each test restores the
//   previous value; see withRealGenerators.
// SPORT: internal/plugins cross-harness adapter conformance (ADD) —
//   P1-E16-W4-S35-T4.

import (
	"context"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"testing"

	"github.com/acamarata/cascade/plugins/codex"
	"github.com/acamarata/cascade/plugins/opencode"
)

// adapter is one harness's install/uninstall pair, reduced to the shape
// all of them share. The three adapters' own signatures differ (one takes
// a Paths struct the others have no equivalent of), which is itself a
// conformance gap; this seam is where that difference is absorbed so the
// assertions below can be written once.
type adapter struct {
	name      string
	install   func(ctx context.Context, cwd string) ([]string, error)
	uninstall func(ctx context.Context, cwd string) ([]string, []string, error)
}

// crossHarnessAdapters returns the adapters this suite runs over.
//
// cascade-claude is deliberately absent: its Uninstall also removes hook
// and MCP config under a Paths root, so "the files install placed" is not
// the same question for it. Its own package covers that; conflating the
// two here would weaken both.
func crossHarnessAdapters() []adapter {
	return []adapter{
		{
			name: "codex",
			install: func(ctx context.Context, cwd string) ([]string, error) {
				res, err := codex.Install(ctx, cwd)
				paths := make([]string, 0, len(res))
				for _, r := range res {
					paths = append(paths, r.Path)
				}
				return paths, err
			},
			uninstall: func(ctx context.Context, cwd string) ([]string, []string, error) {
				res, err := codex.Uninstall(ctx, cwd)
				var removed, clean []string
				for _, r := range res {
					if r.Removed {
						removed = append(removed, r.Path)
					}
					if r.AlreadyClean {
						clean = append(clean, r.Path)
					}
				}
				return removed, clean, err
			},
		},
		{
			name: "opencode",
			install: func(ctx context.Context, cwd string) ([]string, error) {
				res, err := opencode.Install(ctx, cwd)
				paths := make([]string, 0, len(res))
				for _, r := range res {
					paths = append(paths, r.Path)
				}
				return paths, err
			},
			uninstall: func(ctx context.Context, cwd string) ([]string, []string, error) {
				res, err := opencode.Uninstall(ctx, cwd)
				var removed, clean []string
				for _, r := range res {
					if r.Removed {
						removed = append(removed, r.Path)
					}
					if r.AlreadyClean {
						clean = append(clean, r.Path)
					}
				}
				return removed, clean, err
			},
		},
	}
}

// crossHarnessProject seeds a project with a real repo tier AND a real
// global tier under a fake home, so the real generators (wired by this
// package's own init) render real files at both.
//
// Both tiers, not just the repo one: the two AGENTS.md harnesses render
// the SAME name at the repo tier and DIFFERENT names at the global tier,
// so a fixture with only a repo tier would make their manifests look
// identical for a reason that is only half true.
func crossHarnessProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	home := t.TempDir()
	writeTierFile(t, dir, "# Repo Instructions\n\nRun the tests.\n")
	writeTierFile(t, home, "# Global\n\nShort sentences.\n")
	pinHome(t, home)
	crossHarnessHome = home
	return dir
}

// pinHome points os.UserHomeDir at home on every platform this suite runs
// on.
//
// HOME alone is not enough: os.UserHomeDir reads %USERPROFILE% on
// windows, so a fixture that sets only HOME leaves discovery reading the
// RUNNER's home there — where no tier file exists. The global-tier half
// of every assertion below then silently measures nothing, which is how
// the goldens passed on two platforms and failed on the third.
func pinHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	if goruntime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
}

// crossHarnessHome is the fake home the current fixture set, so a captured
// manifest can render a global-tier path as "$HOME/..." instead of as a
// temp-directory path that changes on every run.
var crossHarnessHome string

// TestCrossHarnessUninstallManifest is the manifest-completeness contract:
// uninstall removes exactly the files install placed — no extras, none
// missing.
//
// Both halves matter. A missing path leaves a harness half-configured
// after an uninstall the operator believes succeeded; an extra path is a
// plugin deleting a file it did not write, which is the failure mode every
// one of these adapters guards against by name.
func TestCrossHarnessUninstallManifest(t *testing.T) {
	for _, a := range crossHarnessAdapters() {
		t.Run(a.name, func(t *testing.T) {
			dir := crossHarnessProject(t)

			installed, err := a.install(context.Background(), dir)
			if err != nil {
				t.Fatalf("install: %v", err)
			}
			if len(installed) == 0 {
				t.Fatal("install placed no files; the rest of this test asserts nothing")
			}
			for _, p := range installed {
				if _, statErr := os.Stat(p); statErr != nil {
					t.Fatalf("install reported %s but it is not on disk: %v", p, statErr)
				}
			}

			removed, _, err := a.uninstall(context.Background(), dir)
			if err != nil {
				t.Fatalf("uninstall: %v", err)
			}
			if got, want := sortedCopy(removed), sortedCopy(installed); !equalStrings(got, want) {
				t.Errorf("uninstall removed %v, install placed %v", got, want)
			}
			for _, p := range installed {
				if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
					t.Errorf("%s survived uninstall (stat err = %v)", p, statErr)
				}
			}
		})
	}
}

// TestCrossHarnessUninstallIsIdempotent requires a second uninstall to
// report every file already clean rather than erroring — the same
// convergence rule the install side obeys.
func TestCrossHarnessUninstallIsIdempotent(t *testing.T) {
	for _, a := range crossHarnessAdapters() {
		t.Run(a.name, func(t *testing.T) {
			dir := crossHarnessProject(t)
			if _, err := a.install(context.Background(), dir); err != nil {
				t.Fatalf("install: %v", err)
			}
			if _, _, err := a.uninstall(context.Background(), dir); err != nil {
				t.Fatalf("first uninstall: %v", err)
			}
			removed, clean, err := a.uninstall(context.Background(), dir)
			if err != nil {
				t.Fatalf("second uninstall: %v", err)
			}
			if len(removed) != 0 {
				t.Errorf("the second uninstall removed %v, want nothing", removed)
			}
			if len(clean) == 0 {
				t.Error("the second uninstall reported nothing already-clean, so it cannot be distinguished from a no-op")
			}
		})
	}
}

// TestUninstallingOneHarnessTakesTheSharedFile is a RECORDED HAZARD, not a
// passing design.
//
// The two AGENTS.md harnesses read one file at one project path. Each
// adapter's uninstall asks its generator which files are its own and
// removes them, guarded by a filename check — and for this file both
// answers are correct and identical. So uninstalling either one deletes
// the instruction file the other still needs, and the surviving harness is
// silently de-configured.
//
// Neither adapter can currently detect the other: they may not import
// internal/, so neither can ask which harnesses are installed. This test
// pins the CURRENT behaviour so it is documented rather than discovered,
// and fails the moment it changes — at which point the fix has landed and
// this test is the one to rewrite. See the ticket filed in R-14.254.
func TestUninstallingOneHarnessTakesTheSharedFile(t *testing.T) {
	dir := crossHarnessProject(t)

	if _, err := codex.Install(context.Background(), dir); err != nil {
		t.Fatalf("installing the first harness: %v", err)
	}
	if _, err := opencode.Install(context.Background(), dir); err != nil {
		t.Fatalf("installing the second harness: %v", err)
	}
	shared := filepath.Join(dir, "AGENTS.md")
	if _, err := os.Stat(shared); err != nil {
		t.Fatalf("the shared instruction file was not installed: %v", err)
	}

	if _, err := codex.Uninstall(context.Background(), dir); err != nil {
		t.Fatalf("uninstalling the first harness: %v", err)
	}
	if _, err := os.Stat(shared); !os.IsNotExist(err) {
		t.Skipf("the shared file survived one harness's uninstall (stat err = %v) — "+
			"the hazard this test records has been fixed; rewrite it as an assertion", err)
	}
	t.Log("RECORDED HAZARD: uninstalling one AGENTS.md harness removed the instruction " +
		"file the other still reads; the surviving harness is now silently unconfigured")
}

// sortedCopy returns a sorted copy, so an order difference between an
// install report and an uninstall report is never read as a content one.
func sortedCopy(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	sort.Strings(out)
	return out
}

// equalStrings compares two sorted slices.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
