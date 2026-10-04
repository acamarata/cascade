package build

import (
	"bytes"
	"go/build"
	"go/build/constraint"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

var hygieneShippedRoots = []string{"cmd", "internal", "pkg", "providers", "plugins"}

func hygieneModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("hygiene gate: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("hygiene gate: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

func hygieneIsSkippedDir(name string) bool {
	return name == "testdata" || (name != "." && strings.HasPrefix(name, "."))
}

func hygieneFixtureDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(hygieneModuleRoot(t), "internal", "build", "testdata", "seeded-violations", "hygiene")
}

// --- no-sleep gate -----------------------------------------------------

func hygieneWalkSleep(t *testing.T, root string) []HygieneSleepViolation {
	t.Helper()
	var out []HygieneSleepViolation
	if _, err := os.Stat(root); err != nil {
		return nil
	}
	moduleRoot := hygieneModuleRoot(t)
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if hygieneIsSkippedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(moduleRoot, path)
		if relErr == nil && hygieneIsSleepAllowed(filepath.ToSlash(rel)) {
			return nil
		}
		v, scanErr := NoSleepScanFile(path)
		if scanErr != nil {
			t.Fatalf("hygiene gate: parsing %s: %v", path, scanErr)
		}
		out = append(out, v...)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("hygiene gate: walking %s: %v", root, walkErr)
	}
	return out
}

func TestNoSleepGate_RealTreeGreen(t *testing.T) {
	root := hygieneModuleRoot(t)
	var all []HygieneSleepViolation
	for _, rel := range hygieneShippedRoots {
		all = append(all, hygieneWalkSleep(t, filepath.Join(root, rel))...)
	}
	if len(all) != 0 {
		t.Fatalf("no-sleep gate: %d violation(s) in the real tree: %+v", len(all), all)
	}
}

func TestNoSleepGate_SeededViolationRed_Literal(t *testing.T) {
	fixture := filepath.Join(hygieneFixtureDir(t), "sleep_violation.go")
	v, err := NoSleepScanFile(fixture)
	if err != nil {
		t.Fatalf("no-sleep gate: parsing fixture: %v", err)
	}
	if len(v) == 0 {
		t.Fatal("no-sleep gate: expected a violation in sleep_violation.go, found none")
	}
}

func TestNoSleepGate_SeededViolationRed_Alias(t *testing.T) {
	fixture := filepath.Join(hygieneFixtureDir(t), "sleep_alias_violation.go")
	v, err := NoSleepScanFile(fixture)
	if err != nil {
		t.Fatalf("no-sleep gate: parsing fixture: %v", err)
	}
	if len(v) == 0 {
		t.Fatal("no-sleep gate: expected the aliased tt.Sleep() call to be caught, found none")
	}
}

func TestNoSleepGate_AllowlistedSyncPrefix(t *testing.T) {
	if !hygieneIsSleepAllowed("internal/sync/retry.go") {
		t.Fatal("no-sleep gate: expected internal/sync/ to be on the allowlist")
	}
	if hygieneIsSleepAllowed("internal/fleet/dispatch.go") {
		t.Fatal("no-sleep gate: internal/fleet/ must NOT be allowlisted")
	}
}

// --- no-network-unit-lane check -----------------------------------------

func TestNoNetworkUnitTest_RealTreeGreen(t *testing.T) {
	root := hygieneModuleRoot(t)
	var all []HygieneNetworkImportViolation
	for _, rel := range hygieneShippedRoots {
		full := filepath.Join(root, rel)
		if _, err := os.Stat(full); err != nil {
			continue
		}
		walkErr := filepath.WalkDir(full, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if hygieneIsSkippedDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, "_test.go") {
				return nil
			}
			v, scanErr := NoNetworkUnitTestScanFile(path)
			if scanErr != nil {
				t.Fatalf("no-network gate: parsing %s: %v", path, scanErr)
			}
			all = append(all, v...)
			return nil
		})
		if walkErr != nil {
			t.Fatalf("no-network gate: walking %s: %v", full, walkErr)
		}
	}
	if len(all) != 0 {
		t.Fatalf("no-network-unit-lane gate: %d violation(s) in the real tree: %+v", len(all), all)
	}
}

func TestNoNetworkUnitTest_SeededViolationRed(t *testing.T) {
	fixture := filepath.Join(hygieneFixtureDir(t), "network_import_violation_test.go")
	v, err := NoNetworkUnitTestScanFile(fixture)
	if err != nil {
		t.Fatalf("no-network gate: parsing fixture: %v", err)
	}
	if len(v) == 0 {
		t.Fatal("no-network gate: expected a violation in network_import_violation_test.go, found none")
	}
}

func TestNoNetworkUnitTest_IntegrationTagEscapes(t *testing.T) {
	fixture := filepath.Join(hygieneFixtureDir(t), "network_import_allowed_test.go")
	v, err := NoNetworkUnitTestScanFile(fixture)
	if err != nil {
		t.Fatalf("no-network gate: parsing fixture: %v", err)
	}
	if len(v) != 0 {
		t.Fatalf("no-network gate: expected zero violations for the integration-tagged file, got %+v", v)
	}
}

// TestHygieneIntegrationTagNegationRefused proves the tag check evaluates
// the constraint instead of matching the word: a file only escapes the
// no-network gate when its constraint is false whenever the integration
// tag is unset.
func TestHygieneIntegrationTagNegationRefused(t *testing.T) {
	dir := filepath.Join(hygieneModuleRoot(t), "internal", "build", "testdata", "hygiene")
	cases := map[string]bool{ // fixture -> want a violation
		"negated_integration_test.go":     true,
		"plus_build_negated.txt":          true,
		"integration_or_linux_test.go":    true,
		"short_circuit_test.go":           true,
		"many_tags_test.go":               true,
		"integration_not_windows_test.go": false,
		"header_then_integration_test.go": false,
	}
	for name, wantViolation := range cases {
		v, err := NoNetworkUnitTestScanFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := len(v) == 1 && v[0].Import == "net"; got != wantViolation || (!wantViolation && len(v) != 0) {
			t.Errorf("%s: violations %+v, want violation=%v", name, v, wantViolation)
		}
	}
	if hygieneHasIntegrationTag([]byte("package fixture\n")) {
		t.Error("an unconstrained file counted as integration-tagged")
	}
}

// TestHygieneConstraintHeaderFollowsGoBuild pins the header rules to go/build
// itself: a //go:build line wins over +build lines, and a //go:build inside a
// block comment is no constraint. go/build's MatchFile with no tags is the
// oracle: each fixture builds in the default lane, so its net import must be
// flagged.
func TestHygieneConstraintHeaderFollowsGoBuild(t *testing.T) {
	dir := filepath.Join(hygieneModuleRoot(t), "internal", "build", "testdata", "hygiene")
	for _, name := range []string{"plus_then_gobuild_negated.txt", "block_commented_gobuild_test.go"} {
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		oracle := build.Default
		oracle.BuildTags = nil
		oracle.OpenFile = func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(src)), nil }
		if builds, merr := oracle.MatchFile(dir, "oracle_test.go"); merr != nil || !builds {
			t.Fatalf("%s: go/build MatchFile (no tags) = %v, %v; the fixture must build untagged", name, builds, merr)
		}
		v, err := NoNetworkUnitTestScanFile(filepath.Join(dir, name))
		if err != nil || len(v) != 1 || v[0].Import != "net" {
			t.Errorf("%s: violations %+v (err %v), want the net import flagged", name, v, err)
		}
	}
}

// TestHygieneTagCapFailsClosed asserts the tag-count cap's own verdict and
// reason: a constraint that really needs the integration tag, but names more
// tags than the gate enumerates, is judged untagged. The same shape within
// the cap is tagged, so the refusal comes from the cap alone.
func TestHygieneTagCapFailsClosed(t *testing.T) {
	for n, want := range map[int]bool{2: true, 17: false} {
		tags := make([]string, n)
		for i := range tags {
			tags[i] = "t" + strconv.Itoa(i+1)
		}
		line := "//go:build integration && (" + strings.Join(tags, " || ") + ")"
		expr, err := constraint.Parse(line)
		if err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		needs, reason := hygieneNeedsIntegration(expr, line)
		wantReason := ""
		if !want {
			wantReason = hygieneTooManyTags
		}
		if needs != want || reason != wantReason {
			t.Errorf("%d tags: verdict %v reason %q, want %v %q", n, needs, reason, want, wantReason)
		}
	}
}
