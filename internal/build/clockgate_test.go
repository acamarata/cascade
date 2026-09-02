package build

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// clockgateShippedRoots mirrors stubgateShippedRoots: the shipped-code
// trees the live gate scans.
var clockgateShippedRoots = []string{"cmd", "internal", "pkg", "providers", "plugins"}

// clockgateModuleRoot locates the repo root by walking up from this file.
func clockgateModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("clock gate: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("clock gate: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

// clockgateIsSkippedDir excludes testdata/ (fixtures) and dot dirs.
func clockgateIsSkippedDir(name string) bool {
	return name == "testdata" || (name != "." && strings.HasPrefix(name, "."))
}

// clockgateWalk walks root for non-test, non-exempt .go files and returns
// every denied clock/rand call or dot-import found.
func clockgateWalk(t *testing.T, root string, skipDirs bool) []ClockViolation {
	t.Helper()
	var out []ClockViolation
	if _, err := os.Stat(root); err != nil {
		return nil
	}
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs && clockgateIsSkippedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if ClockgateIsExempt(path) {
			return nil
		}
		v, scanErr := ClockgateScanFile(path)
		if scanErr != nil {
			t.Fatalf("clock gate: parsing %s: %v", path, scanErr)
		}
		out = append(out, v...)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("clock gate: walking %s: %v", root, walkErr)
	}
	return out
}

// TestClockGate_RealTreeGreen: no denied time/rand call or dot-import
// survives in the real tree outside the exempt Clock implementations and
// test files.
func TestClockGate_RealTreeGreen(t *testing.T) {
	root := clockgateModuleRoot(t)
	var all []ClockViolation
	for _, rel := range clockgateShippedRoots {
		all = append(all, clockgateWalk(t, filepath.Join(root, rel), true)...)
	}
	if len(all) != 0 {
		var b strings.Builder
		for _, v := range all {
			b.WriteString(v.File)
			b.WriteString(":")
			b.WriteString(v.Call)
			b.WriteString("\n")
		}
		t.Fatalf("clock gate found %d violation(s) in the real tree:\n%s", len(all), b.String())
	}
}

// TestClockGate_ExemptClocksAreNotScanned proves the two canonical Clock
// implementations, which legitimately call time.Now(), are exempt — a
// regression here would mean the gate is about to break the real Clock
// seams, not catch an evasion.
func TestClockGate_ExemptClocksAreNotScanned(t *testing.T) {
	root := clockgateModuleRoot(t)
	for _, rel := range []string{
		filepath.Join("internal", "runtime", "clock.go"),
		filepath.Join("internal", "testkit", "clock.go"),
	} {
		full := filepath.Join(root, rel)
		if !ClockgateIsExempt(full) {
			t.Errorf("clock gate: expected %s to be exempt, it is not", rel)
		}
	}
}

func clockgateFixtureDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(clockgateModuleRoot(t), "internal", "build", "testdata", "seeded-violations", "clockgate")
}

// TestClockGate_SeededViolationRed_Literal proves the gate catches a
// literal, unaliased time.Now() call — the case forbidigo already catches
// too, proven here so the AST path is confirmed correct on the easy case
// before the harder alias/dot-import cases below.
func TestClockGate_SeededViolationRed_Literal(t *testing.T) {
	fixture := filepath.Join(clockgateFixtureDir(t), "literal_violation.go")
	v, err := ClockgateScanFile(fixture)
	if err != nil {
		t.Fatalf("clock gate: parsing fixture: %v", err)
	}
	var found bool
	for _, viol := range v {
		if viol.Call == "time.Now" {
			found = true
		}
	}
	if !found {
		t.Fatalf("clock gate: expected time.Now in literal_violation.go, got %+v", v)
	}
}

// TestClockGate_SeededViolationRed_AliasEvadesForbidigoNotThisGate is the
// R-14.132 proof: alias_violation.go imports "time" under the alias "t"
// and calls t.Now() — forbidigo's TEXT match on the selector "time.Now"
// cannot see this at all (the selector text is literally "t.Now"), but
// this gate resolves the alias via the file's own import declaration and
// must still flag it as time.Now.
func TestClockGate_SeededViolationRed_AliasEvadesForbidigoNotThisGate(t *testing.T) {
	fixture := filepath.Join(clockgateFixtureDir(t), "alias_violation.go")
	v, err := ClockgateScanFile(fixture)
	if err != nil {
		t.Fatalf("clock gate: parsing fixture: %v", err)
	}
	var found bool
	for _, viol := range v {
		if viol.Call == "time.Now" {
			found = true
		}
	}
	if !found {
		t.Fatalf("clock gate: expected the aliased t.Now() call to resolve to time.Now, got %+v", v)
	}
}

// TestClockGate_SeededViolationRed_DotImport proves the dot-import half:
// dotimport_violation.go dot-imports "time" and calls Now() unqualified,
// which is not even a *ast.SelectorExpr and so cannot be caught by
// selector matching — the gate must reject the dot-import declaration
// itself outright.
func TestClockGate_SeededViolationRed_DotImport(t *testing.T) {
	fixture := filepath.Join(clockgateFixtureDir(t), "dotimport_violation.go")
	v, err := ClockgateScanFile(fixture)
	if err != nil {
		t.Fatalf("clock gate: parsing fixture: %v", err)
	}
	var found bool
	for _, viol := range v {
		if viol.Call == "dot-import:time" {
			found = true
		}
	}
	if !found {
		t.Fatalf("clock gate: expected dot-import:time in dotimport_violation.go, got %+v", v)
	}
}

// TestClockGate_SeededViolationRed_RandAlias proves the same alias
// resolution for math/rand, aliased as "r".
func TestClockGate_SeededViolationRed_RandAlias(t *testing.T) {
	fixture := filepath.Join(clockgateFixtureDir(t), "rand_alias_violation.go")
	v, err := ClockgateScanFile(fixture)
	if err != nil {
		t.Fatalf("clock gate: parsing fixture: %v", err)
	}
	var found bool
	for _, viol := range v {
		if viol.Call == "rand.Intn" {
			found = true
		}
	}
	if !found {
		t.Fatalf("clock gate: expected the aliased r.Intn() call to resolve to rand.Intn, got %+v", v)
	}
}

// TestClockGate_FalsePositive_SeededRandOnValue proves the gate does NOT
// fire on a properly seeded *rand.Rand VALUE's method call — its selector
// base is a local variable, never the "rand" package identifier, so it is
// never even a candidate for aliasToPkg lookup.
func TestClockGate_FalsePositive_SeededRandOnValue(t *testing.T) {
	fixture := filepath.Join(clockgateFixtureDir(t), "clean_seeded_rand.go")
	v, err := ClockgateScanFile(fixture)
	if err != nil {
		t.Fatalf("clock gate: parsing fixture: %v", err)
	}
	if len(v) != 0 {
		t.Fatalf("clock gate: false positive(s) on a seeded *rand.Rand value's method calls: %+v", v)
	}
}
