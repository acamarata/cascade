package build

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stubgateShippedRoots are the shipped-code trees the live gate scans.
var stubgateShippedRoots = []string{"cmd", "internal", "pkg", "providers", "plugins"}

// stubgateSelfExemptRel is this gate's own definition file, repo-relative.
// It is the one file in the entire scanned tree that MUST contain the
// literal denied phrase strings as real Go string-literal DATA
// (stubgateStringPhrases) in order to define what the gate searches for —
// exactly the self-reference problem sweep.go's package doc describes for
// identifier patterns. Every other file in this package, including every
// other file in internal/build/, remains fully scanned; this is a single,
// explicit, documented exemption of one file, not a blanket carve-out.
const stubgateSelfExemptRel = "internal/build/stubgate.go"

// stubgateIsSelfExempt reports whether path (any absolute or root-relative
// form) is the gate's own definition file.
func stubgateIsSelfExempt(path string) bool {
	return strings.HasSuffix(filepath.ToSlash(path), stubgateSelfExemptRel)
}

// stubgateModuleRoot locates the repo root by walking up from this file.
func stubgateModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("stub gate: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("stub gate: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

// stubgateIsSkippedDir excludes testdata/ (fixtures, Art.1/Art.7.1) and dot
// dirs from the live scan.
func stubgateIsSkippedDir(name string) bool {
	return name == "testdata" || (name != "." && strings.HasPrefix(name, "."))
}

// stubgateWalk walks root (a directory, real or a materialized fixture) for
// non-test .go files and returns every unsuppressed Article-1 marker.
func stubgateWalk(t *testing.T, root string, skipTestdata bool) []StubViolation {
	t.Helper()
	var out []StubViolation
	if _, err := os.Stat(root); err != nil {
		return nil
	}
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipTestdata && stubgateIsSkippedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if stubgateIsSelfExempt(path) {
			return nil
		}
		v, scanErr := StubgateScanFile(path)
		if scanErr != nil {
			t.Fatalf("stub gate: reading %s: %v", path, scanErr)
		}
		out = append(out, v...)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("stub gate: walking %s: %v", root, walkErr)
	}
	return out
}

// TestStubGate_RealTreeGreen: no Article-1 marker survives in any shipped
// root of the real tree.
func TestStubGate_RealTreeGreen(t *testing.T) {
	root := stubgateModuleRoot(t)
	var all []StubViolation
	for _, rel := range stubgateShippedRoots {
		all = append(all, stubgateWalk(t, filepath.Join(root, rel), true)...)
	}
	if len(all) != 0 {
		var b strings.Builder
		for _, v := range all {
			b.WriteString(v.File)
			b.WriteString(":")
			b.WriteString(strings.TrimSpace(v.Marker))
			b.WriteString("\n")
		}
		t.Fatalf("stub gate found %d Article-1 marker(s) in the real tree:\n%s", len(all), b.String())
	}
}

func stubgateFixtureDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(stubgateModuleRoot(t), "internal", "build", "testdata", "seeded-violations", "stubgate")
}

// TestStubGate_SeededViolationRed proves every Art.1.2 marker type fires on
// the seeded fixture (violation.go), materialized under t.TempDir()
// (Art.7.1).
func TestStubGate_SeededViolationRed(t *testing.T) {
	src := stubgateFixtureDir(t)
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatalf("stub gate: materializing fixture: %v", err)
	}
	v := stubgateWalk(t, filepath.Join(dst, "violation.go"), false)
	if len(v) == 0 {
		t.Fatal("stub gate: expected violations in seeded fixture violation.go, found none")
	}

	want := map[string]bool{
		"TODO":            false,
		"FIXME":           false,
		"XXX":             false,
		"not implemented": false,
		"unimplemented":   false,
		"placeholder":     false,
		"Mock":            false,
		"Fake":            false,
		"Noop":            false,
	}
	for _, viol := range v {
		for k := range want {
			if strings.Contains(viol.Marker, k) {
				want[k] = true
			}
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("stub gate: seeded fixture expected a %q marker, none found", k)
		}
	}
}

// TestStubGate_CascadeAllowSuppresses proves the escape works: allowed.go
// carries the same markers as violation.go, each preceded by a
// syntactically valid CASCADE-ALLOW comment, and must produce zero
// findings.
func TestStubGate_CascadeAllowSuppresses(t *testing.T) {
	src := stubgateFixtureDir(t)
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatalf("stub gate: materializing fixture: %v", err)
	}
	v := stubgateWalk(t, filepath.Join(dst, "allowed.go"), false)
	if len(v) != 0 {
		t.Fatalf("stub gate: expected zero violations in allowed.go (CASCADE-ALLOW escaped), found %d: %+v", len(v), v)
	}
}

// TestStubGate_FalsePositiveProbes proves the gate does NOT fire on
// legitimate uses: the bare word "stub" in prose, an identifier that merely
// CONTAINS "TODO" as a substring rather than the whole word, and a
// deliberately empty skeleton doc.go.
func TestStubGate_FalsePositiveProbes(t *testing.T) {
	src := stubgateFixtureDir(t)
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatalf("stub gate: materializing fixture: %v", err)
	}
	v := stubgateWalk(t, filepath.Join(dst, "clean.go"), false)
	if len(v) != 0 {
		t.Fatalf("stub gate: false positive(s) on clean.go: %+v", v)
	}
}

// TestStubGate_SkipsTestdata proves the live walk never descends into a
// testdata/ subtree even when one is nested under a scanned root.
func TestStubGate_SkipsTestdata(t *testing.T) {
	root := stubgateModuleRoot(t)
	v := stubgateWalk(t, filepath.Join(root, "internal", "build", "testdata"), true)
	if len(v) != 0 {
		t.Fatalf("stub gate: expected the live walk to skip testdata/ entirely, got %d finding(s)", len(v))
	}
}
