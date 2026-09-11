package build

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// sportgateModuleRoot locates the repo root by walking up from this file,
// matching every other gate's own helper (e.g. countsdriftModuleRoot).
func sportgateModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("sport gate: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("sport gate: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

func sportgateFixtureDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(sportgateModuleRoot(t), "internal", "build", "testdata", "seeded-violations", "sport")
}

// TestSportGate_SeededViolationRed proves the gate catches a bare status
// verb with no extractable entity name (malformed.go).
func TestSportGate_SeededViolationRed(t *testing.T) {
	dir := sportgateFixtureDir(t)
	v, err := CheckSportMalformed(dir, []string{"malformed.go"})
	if err != nil {
		t.Fatalf("sport gate: %v", err)
	}
	if len(v) != 1 {
		t.Fatalf("sport gate: want 1 malformed violation, got %d: %+v", len(v), v)
	}
	if v[0].File != "malformed.go" || v[0].Line != 7 {
		t.Fatalf("sport gate: violation site = %+v, want malformed.go:7", v[0])
	}
}

// TestSportGate_CleanFixtureGreen proves a well-formed marker produces
// zero violations — the false-positive guard.
func TestSportGate_CleanFixtureGreen(t *testing.T) {
	dir := sportgateFixtureDir(t)
	v, err := CheckSportMalformed(dir, []string{"clean.go"})
	if err != nil {
		t.Fatalf("sport gate: %v", err)
	}
	if len(v) != 0 {
		t.Fatalf("sport gate: expected zero violations on the clean fixture, got %+v", v)
	}
}

// TestSportGate_RealTreeGreen scans every git-tracked .go file (skipping
// testdata/, per SportGateSkipsPath) and asserts zero malformed SPORT
// lines — the CI-facing half.
func TestSportGate_RealTreeGreen(t *testing.T) {
	root := sportgateModuleRoot(t)
	all, err := ListTrackedFiles(root)
	if err != nil {
		t.Fatalf("sport gate: %v", err)
	}
	var files []string
	for _, f := range all {
		if !strings.HasSuffix(f, ".go") || SportGateSkipsPath(f) {
			continue
		}
		files = append(files, f)
	}
	v, err := CheckSportMalformed(root, files)
	if err != nil {
		t.Fatalf("sport gate: %v", err)
	}
	if len(v) != 0 {
		t.Fatalf("sport gate: real tree has %d malformed SPORT line(s):\n%v", len(v), v)
	}
}

// TestSportGate_SkipsTestdata proves the real-tree scan's testdata
// exclusion actually excludes this gate's own seeded fixtures: without
// it, TestSportGate_RealTreeGreen above would be permanently red on its
// own malformed.go fixture.
func TestSportGate_SkipsTestdata(t *testing.T) {
	if !SportGateSkipsPath("internal/build/testdata/seeded-violations/sport/malformed.go") {
		t.Fatal("sport gate: expected a testdata/ path to be skipped, it is not")
	}
}

// TestSportGate_MissingFileSkipped proves a tracked path that no longer
// exists on disk (concurrent-agent working-tree churn) is skipped, never
// a hard error — CheckSportMalformed delegates straight to
// sport.ScanFiles, which already has this behavior; this test pins that
// the gate wrapper does not reintroduce a hard failure.
func TestSportGate_MissingFileSkipped(t *testing.T) {
	dir := sportgateFixtureDir(t)
	v, err := CheckSportMalformed(dir, []string{"does-not-exist.go"})
	if err != nil {
		t.Fatalf("sport gate: expected missing file to be skipped, got error: %v", err)
	}
	if len(v) != 0 {
		t.Fatalf("sport gate: expected zero violations for a missing file, got %+v", v)
	}
}
