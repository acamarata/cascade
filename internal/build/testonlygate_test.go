package build

import (
	"path/filepath"
	"strings"
	"testing"
)

// testOnlyExemptDirs are package trees whose whole purpose is to be called
// from tests. Flagging them would report the design as a defect and drown
// the real findings.
//
//   - internal/build is this gate and its siblings. The gates ARE tests, so
//     every exported symbol here is called from a _test.go file by design.
//   - internal/testkit and internal/storage/storetest are shared test
//     infrastructure, published for other packages' tests to consume.
//   - pkg is the public API. Its consumers are outside this repository
//     entirely, so "nothing in this tree calls it" is expected rather than
//     suspicious. The dead-code gate already applies its own SDK-intent rule
//     there.
var testOnlyExemptDirs = []string{
	"internal/build",
	"internal/testkit",
	"internal/storage/storetest",
	"pkg",
}

func testOnlyExempt(dir string) bool {
	for _, p := range testOnlyExemptDirs {
		if dir == p || strings.HasPrefix(dir, p+"/") {
			return true
		}
	}
	return false
}

// TestTestOnlyUsage_RealTreeGreen catches the failure the dead-code gate
// structurally cannot see.
//
// That gate counts a symbol as used when anything references it, including
// its own tests, which is correct for finding dead code. It means a
// subsystem that is fully built, thoroughly tested, and called by nothing
// that ships looks alive to it. Several such subsystems reached this
// repository's main branch, each with green tests, and none were caught by
// a gate.
//
// This test asks whether a symbol is referenced ONLY from test files. A yes
// is not automatically wrong: a guard written before the operation it guards
// is a reasonable order to build in. What is not acceptable is for it to be
// invisible, so each instance must appear in the allow list with a reason
// and the caller that is expected to appear.
func TestTestOnlyUsage_RealTreeGreen(t *testing.T) {
	root := deadcodeModuleRoot(t)
	found, err := ScanTestOnlySymbols(root, deadcodeModulePath, deadcodeShippedRoots)
	if err != nil {
		t.Fatalf("test-only gate: scanning real tree: %v", err)
	}

	var candidates []TestOnlySymbol
	for _, s := range found {
		if testOnlyExempt(s.Dir) {
			continue
		}
		candidates = append(candidates, s)
	}

	allow, err := LoadTestOnlyAllowList(filepath.Join(root, "internal", "build", "testonly-allow.json"))
	if err != nil {
		t.Fatal(err) // fail closed: an unreadable allow list blocks, never skips
	}

	violations, stale := FilterTestOnlyAllowed(candidates, allow)
	for _, s := range stale {
		t.Errorf("test-only gate: allow-list entry %q matches nothing. It is either wired now, in which "+
			"case delete the entry, or renamed, in which case fix it. A stale exemption hides the next one.", s)
	}
	if len(violations) > 0 {
		var b strings.Builder
		for _, v := range violations {
			b.WriteString("\n  " + v.Dir + "." + v.Name)
		}
		t.Fatalf("test-only gate: %d symbol(s) shipped but referenced only by tests:%s\n\n"+
			"Each one is built and tested but unreachable from the running program. Either wire it to a "+
			"production caller, or add it to internal/build/testonly-allow.json with a reason and the "+
			"caller that is expected to appear.", len(violations), b.String())
	}
}

// TestTestOnlyAllowList_RejectsAnEntryWithoutAReason stops the allow list
// from degrading into a list of names. An exemption with no reason and no
// named caller cannot be retired by anyone later, which makes it permanent.
func TestTestOnlyAllowList_RejectsAnEntryWithoutAReason(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allow.json")
	writeFileT(t, path, `[{"symbol":"internal/x.Y","reason":"","expected_caller":"z"}]`)
	if _, err := LoadTestOnlyAllowList(path); err == nil {
		t.Error("an entry with no reason must be rejected")
	}
	writeFileT(t, path, `[{"symbol":"internal/x.Y","reason":"r","expected_caller":""}]`)
	if _, err := LoadTestOnlyAllowList(path); err == nil {
		t.Error("an entry naming no expected caller must be rejected")
	}
}

// TestTestOnlyAllowList_MissingFileIsEmptyNotAnError keeps the strictest
// state the easiest one to be in.
func TestTestOnlyAllowList_MissingFileIsEmptyNotAnError(t *testing.T) {
	allow, err := LoadTestOnlyAllowList(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("a missing allow list must be empty, not an error: %v", err)
	}
	if len(allow) != 0 {
		t.Fatalf("expected no entries, got %d", len(allow))
	}
}
