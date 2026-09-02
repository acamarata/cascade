package build

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

var deadcodeShippedRoots = []string{"cmd", "internal", "pkg", "providers", "plugins"}

const deadcodeModulePath = "github.com/acamarata/cascade"

func deadcodeModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("dead-code gate: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("dead-code gate: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

// TestDeadCode_RealTreeGreen: zero unused exported internal/ symbols and
// zero unused pkg/ symbols missing an SDK-intent note, across the whole
// real tree (cmd, internal, pkg, providers, plugins) as it stands today.
func TestDeadCode_RealTreeGreen(t *testing.T) {
	root := deadcodeModuleRoot(t)
	declared, used, err := ScanModuleSymbols(root, deadcodeModulePath, deadcodeShippedRoots)
	if err != nil {
		t.Fatalf("dead-code gate: scanning real tree: %v", err)
	}
	if len(declared) == 0 {
		t.Fatal("dead-code gate: scanned zero declared symbols — gate regression, not a legitimately empty tree")
	}

	if v := InternalDeadCodeViolations(declared, used); len(v) != 0 {
		t.Errorf("dead-code gate: %d unused internal/ symbol(s): %+v", len(v), v)
	}
	if v := PkgUnusedWithoutSDKIntent(declared, used); len(v) != 0 {
		t.Errorf("dead-code gate: %d unused pkg/ symbol(s) missing an SDK-intent note: %+v", len(v), v)
	}
}

func deadcodeFixtureModule(t *testing.T) string {
	t.Helper()
	return filepath.Join(deadcodeModuleRoot(t), "internal", "build", "testdata", "seeded-violations", "deadcode", "module")
}

const deadcodeFixtureModulePath = "example.com/fixture"

// TestDeadCode_SeededViolationRed_InternalUnused proves the internal/
// hard-fail half: DeadFunc is declared and never referenced anywhere in
// the fixture module.
func TestDeadCode_SeededViolationRed_InternalUnused(t *testing.T) {
	root := deadcodeFixtureModule(t)
	declared, used, err := ScanModuleSymbols(root, deadcodeFixtureModulePath, []string{"internal", "pkg"})
	if err != nil {
		t.Fatalf("dead-code gate: scanning fixture: %v", err)
	}
	v := InternalDeadCodeViolations(declared, used)
	var found bool
	for _, viol := range v {
		if viol.Name == "DeadFunc" {
			found = true
		}
		if viol.Name == "UsedFunc" {
			t.Errorf("dead-code gate: UsedFunc is referenced from internal/other and must not be flagged")
		}
	}
	if !found {
		t.Fatalf("dead-code gate: expected DeadFunc to be flagged, got %+v", v)
	}
}

// TestDeadCode_SeededViolationRed_SDKIntentMissing proves the pkg/
// SDK-intent half: NoIntentUnused is unused and undocumented as intentional
// API, while ExportedWithIntent is equally unused but carries the
// SDK-INTENT marker and must be exempt.
func TestDeadCode_SeededViolationRed_SDKIntentMissing(t *testing.T) {
	root := deadcodeFixtureModule(t)
	declared, used, err := ScanModuleSymbols(root, deadcodeFixtureModulePath, []string{"internal", "pkg"})
	if err != nil {
		t.Fatalf("dead-code gate: scanning fixture: %v", err)
	}
	v := PkgUnusedWithoutSDKIntent(declared, used)
	var found bool
	for _, viol := range v {
		if viol.Name == "NoIntentUnused" {
			found = true
		}
		if viol.Name == "ExportedWithIntent" {
			t.Errorf("dead-code gate: ExportedWithIntent carries an SDK-INTENT marker and must not be flagged")
		}
	}
	if !found {
		t.Fatalf("dead-code gate: expected NoIntentUnused to be flagged, got %+v", v)
	}
}

// --- godoc gate ----------------------------------------------------------

func TestGodocGate_RealTreeGreen(t *testing.T) {
	root := deadcodeModuleRoot(t)
	v, err := ScanPkgGodoc(filepath.Join(root, "pkg"))
	if err != nil {
		t.Fatalf("godoc gate: %v", err)
	}
	if len(v) != 0 {
		t.Fatalf("godoc gate: %d undocumented exported pkg/ symbol(s): %+v", len(v), v)
	}
}

func TestGodocGate_SeededViolationRed(t *testing.T) {
	fixture := filepath.Join(deadcodeModuleRoot(t), "internal", "build", "testdata", "seeded-violations", "godoc")
	v, err := ScanPkgGodoc(fixture)
	if err != nil {
		t.Fatalf("godoc gate: %v", err)
	}
	names := map[string]bool{}
	for _, viol := range v {
		names[viol.Name] = true
	}
	if !names["UndocumentedFunc"] || !names["UndocumentedType"] {
		t.Fatalf("godoc gate: expected UndocumentedFunc and UndocumentedType to be flagged, got %+v", v)
	}
	if names["DocumentedFunc"] {
		t.Fatalf("godoc gate: DocumentedFunc has a doc comment and must not be flagged, got %+v", v)
	}
}

func TestGodocGate_ExampleRealTreeGreen(t *testing.T) {
	root := deadcodeModuleRoot(t)
	v, err := ScanPkgExamples(filepath.Join(root, "pkg"))
	if err != nil {
		t.Fatalf("godoc example gate: %v", err)
	}
	if len(v) != 0 {
		t.Fatalf("godoc example gate: %d pkg/ subpackage(s) with an exported func but no Example: %+v", len(v), v)
	}
}

func TestGodocGate_ExampleSeededViolationRed(t *testing.T) {
	fixture := filepath.Join(deadcodeModuleRoot(t), "internal", "build", "testdata", "seeded-violations", "godoc", "no-example-module", "pkg")
	v, err := ScanPkgExamples(fixture)
	if err != nil {
		t.Fatalf("godoc example gate: %v", err)
	}
	if len(v) == 0 {
		t.Fatal("godoc example gate: expected nofixture to be flagged for missing an Example, found none")
	}
}

func TestGodocGate_ExampleSeededPositiveControl(t *testing.T) {
	fixture := filepath.Join(deadcodeModuleRoot(t), "internal", "build", "testdata", "seeded-violations", "godoc", "has-example-module", "pkg")
	v, err := ScanPkgExamples(fixture)
	if err != nil {
		t.Fatalf("godoc example gate: %v", err)
	}
	if len(v) != 0 {
		t.Fatalf("godoc example gate: hasfixture ships ExampleDoThing and must not be flagged, got %+v", v)
	}
}
