package plugins

// Purpose: the shared-instruction-file rule (P1-E16-W4-S35-T8, R-14.265)
//   -- uninstalling one of two harnesses that read the same project file
//   keeps that file while the other is installed, and a full uninstall
//   still leaves nothing behind. Split from cross_harness_test.go for the
//   300-line cap.
// Constraints: these drive the REAL detector over a pinned HOME. A
//   hand-built shared set would pass against an adapter that never asked
//   anything, which is the stub this rule exists to avoid.
// SPORT: internal/plugins shared instruction paths (ADD) --
//   P1-E16-W4-S35-T8.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	casctx "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/plugins/codex"
	"github.com/acamarata/cascade/plugins/opencode"
)

// TestUninstallingOneHarnessLeavesTheOtherConfigured replaces the
// recorded hazard R-14.254 left here.
//
// The two AGENTS.md harnesses read one file at one project path. Each
// adapter's uninstall asks its generator which files are its own and gets
// that file — both answers correct and identical — so uninstalling either
// one used to delete the instruction file the other still needs.
//
// Neither adapter can detect the other: plugins/** may not import
// internal/**. So the composition root computes which paths an installed
// harness still claims and passes the set in (R-14.265). This test drives
// that computation through the REAL detector over a pinned HOME, not a
// hand-built set — a hardcoded map here would pass against an adapter that
// never asked anything.
func TestUninstallingOneHarnessLeavesTheOtherConfigured(t *testing.T) {
	dir := crossHarnessProject(t)
	seedHarnessRoots(t)

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

	claimed, err := sharedPathsFor(context.Background(), hostHarnessDetector(), casctx.HarnessCodex, dir)
	if err != nil {
		t.Fatalf("computing the shared set: %v", err)
	}
	if claimed[shared] == "" {
		t.Fatalf("the shared set does not name %s as claimed by another harness: %v", shared, claimed)
	}

	results, err := codex.Uninstall(context.Background(), dir, claimed)
	if err != nil {
		t.Fatalf("uninstalling the first harness: %v", err)
	}
	if _, err := os.Stat(shared); err != nil {
		t.Fatalf("the surviving harness's instruction file was removed anyway: %v", err)
	}
	if !keptPath(results, shared) {
		t.Errorf("the shared file was left on disk but not REPORTED as kept: %+v; "+
			"an operator who is not told cannot act on it", results)
	}
}

// TestUninstallingTheLastHarnessLeavesNothingBehind is the other half.
// Keeping a file for a harness that is not installed would leave cascade's
// own file on every machine, which is the failure the keep rule must not
// trade for.
func TestUninstallingTheLastHarnessLeavesNothingBehind(t *testing.T) {
	dir := crossHarnessProject(t)
	seedHarnessRoots(t)

	if _, err := codex.Install(context.Background(), dir); err != nil {
		t.Fatalf("installing codex: %v", err)
	}
	if _, err := opencode.Install(context.Background(), dir); err != nil {
		t.Fatalf("installing opencode: %v", err)
	}
	shared := filepath.Join(dir, "AGENTS.md")

	claimed, err := sharedPathsFor(context.Background(), hostHarnessDetector(), casctx.HarnessCodex, dir)
	if err != nil {
		t.Fatalf("computing the shared set: %v", err)
	}
	if _, err := codex.Uninstall(context.Background(), dir, claimed); err != nil {
		t.Fatalf("uninstalling codex: %v", err)
	}

	// The second uninstall runs with codex gone, so nothing claims the
	// file any more. Recomputed rather than reused: a stale set is how a
	// full uninstall would leave litter behind.
	removeHarnessRoot(t, ".codex")
	claimed, err = sharedPathsFor(context.Background(), hostHarnessDetector(), casctx.HarnessOpenCode, dir)
	if err != nil {
		t.Fatalf("recomputing the shared set: %v", err)
	}
	if claimed[shared] != "" {
		t.Fatalf("%s is still claimed after the only other harness was removed: %v", shared, claimed)
	}
	if _, err := opencode.Uninstall(context.Background(), dir, claimed); err != nil {
		t.Fatalf("uninstalling opencode: %v", err)
	}
	if _, err := os.Stat(shared); !os.IsNotExist(err) {
		t.Errorf("a full uninstall left %s behind (stat err = %v)", shared, err)
	}
}

// keptPath reports whether results name path as deliberately kept.
func keptPath(results []codex.UninstallResult, path string) bool {
	for _, r := range results {
		if r.Path == path && r.Kept && r.KeptReason != "" {
			return true
		}
	}
	return false
}

// seedHarnessRoots creates the config directories the real detector probes
// for, so "installed" in these tests means what it means in production.
func seedHarnessRoots(t *testing.T) {
	t.Helper()
	for _, dir := range []string{".codex", filepath.Join(".config", "opencode")} {
		if err := os.MkdirAll(filepath.Join(crossHarnessHome, dir), 0o750); err != nil {
			t.Fatalf("seeding %s: %v", dir, err)
		}
	}
}

// removeHarnessRoot deletes one seeded harness root, so the detector stops
// reporting it installed.
func removeHarnessRoot(t *testing.T, dir string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(crossHarnessHome, dir)); err != nil {
		t.Fatalf("removing %s: %v", dir, err)
	}
}
