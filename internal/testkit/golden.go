package testkit

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Purpose: load-and-compare helper for golden-fixture tests, plus a guarded
//   update mode. This is the plan's standard verification currency from
//   B/S-02.T3 onward (06-FORGE-SPEC.md §5.12); v1-goldens harvested by
//   later tickets follow the same discipline this file establishes.
// Inputs: a testing.TB, a fixture name, and deterministically-rendered
//   bytes to compare (or, in update mode, to persist).
// Outputs: the golden file's bytes (want, in compare mode; the freshly
//   written got, in update mode); test failures reported via t.
// Constraints: Art.7.1 (this file itself writes nothing outside a golden
//   fixture path the caller owns; it never touches t.TempDir() itself —
//   callers render `got` however they like, including via t.TempDir()).
//   Art.7.3: deterministic — no wall clock, no randomness in this helper.
// SPORT: build/testkit (ADD, placeholder per T-4 sport_updates).

// updateEnvVar, when set to "1", switches Golden into update mode: it
// writes `got` to the golden file instead of comparing. This is the ONLY
// switch — there is no separate flag.Bool, deliberately: a package-global
// Go test flag registered from a non-test file would collide across any
// test binary that imports testkit alongside another "-update" flag, and
// would still need this same CI guard. The env var is the whole mechanism.
const updateEnvVar = "CASCADE_TESTKIT_UPDATE_GOLDEN"

// ciEnvVar is set (to any non-empty value) by GitHub Actions on every job,
// and is the standard convention for "running under CI" more broadly. It is
// the guard that makes it impossible to accidentally "fix" a failing golden
// test by having CI regenerate the golden it was supposed to check against.
const ciEnvVar = "CI"

// goldenDir is the conventional subdirectory of a package's testdata/ that
// holds golden fixtures, matching the layout already established by
// internal/runtime/testdata/goldens/*.golden.
const goldenDir = "goldens"

// UpdateRequested reports whether golden update mode was requested via
// updateEnvVar, without applying the CI guard. Exported so callers can
// short-circuit expensive rendering when a plain comparison is all that
// will happen; Golden itself is what actually enforces the CI guard.
func UpdateRequested() bool {
	return os.Getenv(updateEnvVar) == "1"
}

// goldenPath returns the on-disk path for the golden fixture named name,
// relative to the current package's testdata directory (Go test always
// runs with the package directory as its working directory).
func goldenPath(name string) string {
	return filepath.Join("testdata", goldenDir, name+".golden")
}

// Golden compares got against the golden fixture testdata/goldens/<name>.golden
// in the calling package. In update mode (updateEnvVar=1) it writes got to
// that path instead — unless ciEnvVar is also set, in which case it fails
// the test outright rather than silently regenerating a golden inside CI.
//
// Golden returns the golden file's bytes: in compare mode, what was read
// (want); in update mode, what was just written (got).
func Golden(t testing.TB, name string, got []byte) []byte {
	t.Helper()
	path := goldenPath(name)

	if UpdateRequested() {
		if os.Getenv(ciEnvVar) != "" {
			t.Fatalf("testkit: refusing to update golden %q: %s=1 was set but so was %s — goldens are never regenerated in CI (internal/testkit/golden.go)", path, updateEnvVar, ciEnvVar)
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("testkit: creating golden dir for %q: %v", path, err)
			return nil
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("testkit: writing golden %q: %v", path, err)
			return nil
		}
		t.Logf("testkit: updated golden %q (%s=1)", path, updateEnvVar)
		return got
	}

	want, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Fatalf("testkit: golden fixture %q does not exist (set %s=1 locally, never in CI, to create it)", path, updateEnvVar)
			return nil
		}
		t.Fatalf("testkit: reading golden %q: %v", path, err)
		return nil
	}
	if !bytes.Equal(want, got) {
		t.Errorf("testkit: golden mismatch for %q\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
	return want
}

// MustRenderLine is a tiny deterministic-rendering convenience used by the
// AC sample golden test: it formats a name/value pair as a stable, single
// trailing-newline-terminated line, with no wall-clock or randomness
// involvement, so its output is safe golden-test material on its own.
func MustRenderLine(name string, value int) []byte {
	return []byte(fmt.Sprintf("%s=%d\n", name, value))
}
