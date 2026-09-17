package plugins

// Purpose: the committed uninstall-manifest goldens (P1-E16-W4-S35-T4).
//   Split from cross_harness_test.go so that file stays the behavioural
//   assertions and this stays the captured artifact, and so both meet
//   Art.10.3's 300-line cap.
// SPORT: internal/plugins cross-harness adapter conformance (ADD) —
//   P1-E16-W4-S35-T4.

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// updateManifests re-captures the committed uninstall manifests from a
// real install run, rather than asserting against them.
var updateManifests = flag.Bool("update-uninstall-manifests", false,
	"re-capture internal/plugins/testdata/cross-harness/*/uninstall-manifest.golden")

// TestCrossHarnessUninstallManifestGolden pins WHICH files each adapter
// claims, as a committed artifact captured from a real install.
//
// The manifest is stored project-relative: an absolute temp path would
// make the golden unreadable and un-diffable, and the interesting fact is
// the shape of the path, not where the test ran.
func TestCrossHarnessUninstallManifestGolden(t *testing.T) {
	for _, a := range crossHarnessAdapters() {
		t.Run(a.name, func(t *testing.T) {
			dir := crossHarnessProject(t)
			installed, err := a.install(context.Background(), dir)
			if err != nil {
				t.Fatalf("install: %v", err)
			}
			rel := make([]string, 0, len(installed))
			for _, p := range installed {
				rel = append(rel, manifestPath(dir, p))
			}
			body := strings.Join(sortedCopy(rel), "\n") + "\n"

			golden := filepath.Join("testdata", "cross-harness", a.name, "uninstall-manifest.golden")
			if *updateManifests {
				if writeErr := os.WriteFile(golden, []byte(body), 0o600); writeErr != nil {
					t.Fatalf("re-capturing %s: %v", golden, writeErr)
				}
				return
			}
			want, readErr := os.ReadFile(golden) //nolint:gosec // fixed corpus path.
			if readErr != nil {
				t.Fatalf("reading %s: %v (re-capture with -update-uninstall-manifests)", golden, readErr)
			}
			if body != string(want) {
				t.Errorf("%s claims different files than its committed manifest.\n--- got ---\n%s--- want ---\n%s",
					a.name, body, want)
			}
		})
	}
}

// manifestPath renders one installed path for a committed manifest.
//
// A path inside the project is project-relative; a path inside the fake
// home is "$HOME/..."; anything else is recorded verbatim. A path outside
// both roots is exactly what a manifest golden is for, so it is written
// down rather than hidden behind a relativization failure.
func manifestPath(project, p string) string {
	if r, err := filepath.Rel(project, p); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	if crossHarnessHome != "" {
		if r, err := filepath.Rel(crossHarnessHome, p); err == nil && !strings.HasPrefix(r, "..") {
			return "$HOME/" + filepath.ToSlash(r)
		}
	}
	return filepath.ToSlash(p)
}
