package build

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// countsdriftModuleRoot locates the repo root by walking up from this
// file, matching every other gate's own helper (e.g. clockgateModuleRoot).
func countsdriftModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("counts drift gate: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("counts drift gate: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

func countsdriftFixtureDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(countsdriftModuleRoot(t), "internal", "build", "testdata", "seeded-violations", "countsdrift")
}

func countsdriftAssertFound(t *testing.T, v []CountDriftViolation, noun string, stated int) {
	t.Helper()
	for _, viol := range v {
		if viol.Noun == noun && viol.Stated == stated {
			return
		}
	}
	t.Fatalf("counts drift gate: expected a %s violation stating %d, got %+v", noun, stated, v)
}

// TestCountsDriftGate_SeededViolationRed_Digit proves the gate catches a
// digit-form mismatch ("13-kind" against the real 14-member taxonomy).
func TestCountsDriftGate_SeededViolationRed_Digit(t *testing.T) {
	fixtureDir := countsdriftFixtureDir(t)
	rel := "digit_violation.go"
	v, err := CheckCountsDrift(fixtureDir, []string{rel})
	if err != nil {
		t.Fatalf("counts drift gate: %v", err)
	}
	countsdriftAssertFound(t, v, "kind", 13)
}

// TestCountsDriftGate_SeededViolationRed_Word proves the gate catches both
// an English-word kind mismatch ("thirteen kinds") and an English-word
// domain mismatch ("twelve domains") in the same file.
func TestCountsDriftGate_SeededViolationRed_Word(t *testing.T) {
	fixtureDir := countsdriftFixtureDir(t)
	rel := "word_violation.md"
	v, err := CheckCountsDrift(fixtureDir, []string{rel})
	if err != nil {
		t.Fatalf("counts drift gate: %v", err)
	}
	countsdriftAssertFound(t, v, "kind", 13)
	countsdriftAssertFound(t, v, "domain", 12)
}

// TestCountsDriftGate_CleanFixtureGreen proves a file stating the CORRECT
// numbers produces zero violations — the gate's false-positive guard.
func TestCountsDriftGate_CleanFixtureGreen(t *testing.T) {
	fixtureDir := countsdriftFixtureDir(t)
	v, err := CheckCountsDrift(fixtureDir, []string{"clean.go"})
	if err != nil {
		t.Fatalf("counts drift gate: %v", err)
	}
	if len(v) != 0 {
		t.Fatalf("counts drift gate: expected zero violations on the clean fixture, got %+v", v)
	}
}

// TestCountsDriftGate_RealTreeGreen scans the entire real, tracked tree
// (skipping testdata/, per CountsDriftSkipsPath) and asserts zero
// violations. This is the CI-facing half: every one of the eight sites
// AGENT-BRIEF named (providers/openai/auth.go, internal/context's two
// error files, internal/memory/store.go, internal/providers/intake/errors.go,
// internal/output/exitcodes_test.go, internal/nodes/records.go,
// CHANGELOG.md) states "14-kind" or "eleven domain(s)" and every one of
// them was verified, by this exact test, to already be correct — this
// gate's value is that the NEXT taxonomy or domain-set amendment which
// forgets one of them now fails CI instead of shipping silently wrong.
func TestCountsDriftGate_RealTreeGreen(t *testing.T) {
	root := countsdriftModuleRoot(t)
	files, err := ListTrackedFiles(root)
	if err != nil {
		t.Fatalf("counts drift gate: %v", err)
	}
	v, err := CheckCountsDrift(root, files)
	if err != nil {
		t.Fatalf("counts drift gate: %v", err)
	}
	if len(v) != 0 {
		t.Fatalf("counts drift gate: real tree has %d unresolved drift violation(s):\n%v", len(v), v)
	}
}

// TestCountsDriftGate_SkipsTestdata proves the real-tree scan's testdata
// exclusion actually excludes this gate's own seeded fixtures: without it,
// TestCountsDriftGate_RealTreeGreen above would be permanently red on its
// own test data.
func TestCountsDriftGate_SkipsTestdata(t *testing.T) {
	if !CountsDriftSkipsPath("internal/build/testdata/seeded-violations/countsdrift/digit_violation.go") {
		t.Fatal("counts drift gate: expected a testdata/ path to be skipped, it is not")
	}
}

// TestCountsDriftGate_UnknownWordSkipped proves a number word this gate
// does not recognize (beyond its zero-twenty table) is skipped rather than
// misparsed as zero, which would falsely flag every legitimate "kind" or
// "domain" usage nearby.
func TestCountsDriftGate_UnknownWordSkipped(t *testing.T) {
	v := countsDriftScanContent(CountsDriftNounCounts(), "fixture.go", []byte("a hundred-kind taxonomy"))
	if len(v) != 0 {
		t.Fatalf("counts drift gate: expected an unrecognized number word to be skipped, got %+v", v)
	}
}

// TestCountsDriftGate_MissingFileSkipped proves a tracked path that no
// longer exists on disk (concurrent-agent working-tree churn) is skipped,
// never a hard error.
func TestCountsDriftGate_MissingFileSkipped(t *testing.T) {
	fixtureDir := countsdriftFixtureDir(t)
	v, err := CheckCountsDrift(fixtureDir, []string{"does-not-exist.go"})
	if err != nil {
		t.Fatalf("counts drift gate: expected missing file to be skipped, got error: %v", err)
	}
	if len(v) != 0 {
		t.Fatalf("counts drift gate: expected zero violations for a missing file, got %+v", v)
	}
}

// TestCountsDriftGate_ExemptionsAreLive proves every CountsDriftExemptions
// entry still points at a real line that still produces the exact match it
// was exempted for — a stale exemption (the line moved, or no longer
// states that number) would otherwise silently widen what this gate
// accepts, exactly the failure mode SecretScanExemptions guards against.
func TestCountsDriftGate_ExemptionsAreLive(t *testing.T) {
	root := countsdriftModuleRoot(t)
	nounCounts := CountsDriftNounCounts()
	for key := range CountsDriftExemptions {
		parts := strings.Split(key, ":")
		if len(parts) != 2 {
			t.Fatalf("counts drift gate: malformed exemption key %q", key)
		}
		rel, lineNo := parts[0], parts[1]
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("counts drift gate: exemption %q: %v", key, err)
		}
		found := countsDriftScanContent(nounCounts, rel, data)
		matched := false
		for _, v := range found {
			if fmt.Sprintf("%d", v.Line) == lineNo {
				matched = true
			}
		}
		if !matched {
			t.Fatalf("counts drift gate: exemption %q no longer matches anything at that line; remove or update it", key)
		}
	}
}

// TestCountsDriftGate_UnscannedExtensionSkipped proves a file extension
// outside countsDriftScannedExt is never read, even if it would otherwise
// contain a mismatched count.
func TestCountsDriftGate_UnscannedExtensionSkipped(t *testing.T) {
	fixtureDir := countsdriftFixtureDir(t)
	v, err := CheckCountsDrift(fixtureDir, []string{"digit_violation.go.bin"})
	if err != nil {
		t.Fatalf("counts drift gate: %v", err)
	}
	if len(v) != 0 {
		t.Fatalf("counts drift gate: expected zero violations for an unscanned extension, got %+v", v)
	}
}
