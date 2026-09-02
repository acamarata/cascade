// Package build (this file) holds outputgate.go's seeded-violation RED
// proofs and its false-positive GREEN proofs, split out of outputgate_test.go
// the same way boundary_seeded_test.go splits from boundary_test.go
// (R-14.117).
package build

import (
	"path/filepath"
	"testing"
)

func outputgateAssertFound(t *testing.T, v []OutputViolation, want string) {
	t.Helper()
	for _, viol := range v {
		if viol.Call == want {
			return
		}
	}
	t.Fatalf("output gate: expected %s, got %+v", want, v)
}

// TestOutputGate_SeededViolationRed_Literal proves the gate catches a
// literal, unaliased os.Stdout reference.
func TestOutputGate_SeededViolationRed_Literal(t *testing.T) {
	fixture := filepath.Join(outputgateFixtureDir(t), "literal_violation.go")
	v, err := OutputgateScanFile(fixture)
	if err != nil {
		t.Fatalf("output gate: parsing fixture: %v", err)
	}
	outputgateAssertFound(t, v, "os.Stdout")
}

// TestOutputGate_SeededViolationRed_AliasEvadesForbidigoNotThisGate is the
// R-14.137 alias proof: alias_violation.go imports "os" under the alias
// "osalias" — forbidigo's TEXT match on "os.Stdout" cannot see this (the
// selector text is literally "osalias.Stdout"), but this gate resolves the
// alias via the file's own import declaration and must still flag it.
func TestOutputGate_SeededViolationRed_AliasEvadesForbidigoNotThisGate(t *testing.T) {
	fixture := filepath.Join(outputgateFixtureDir(t), "alias_violation.go")
	v, err := OutputgateScanFile(fixture)
	if err != nil {
		t.Fatalf("output gate: parsing fixture: %v", err)
	}
	outputgateAssertFound(t, v, "os.Stdout")
}

// TestOutputGate_SeededViolationRed_DotImportOS proves the os dot-import
// half: dotimport_violation.go dot-imports "os" and calls Stdout.Write
// unqualified, which is not even a *ast.SelectorExpr — the gate must
// reject the dot-import declaration itself.
func TestOutputGate_SeededViolationRed_DotImportOS(t *testing.T) {
	fixture := filepath.Join(outputgateFixtureDir(t), "dotimport_violation.go")
	v, err := OutputgateScanFile(fixture)
	if err != nil {
		t.Fatalf("output gate: parsing fixture: %v", err)
	}
	outputgateAssertFound(t, v, "dot-import:os")
}

// TestOutputGate_SeededViolationRed_DotImportFmt proves the dot-import
// rule also covers "fmt", not only "os".
func TestOutputGate_SeededViolationRed_DotImportFmt(t *testing.T) {
	fixture := filepath.Join(outputgateFixtureDir(t), "dotimport_fmt_violation.go")
	v, err := OutputgateScanFile(fixture)
	if err != nil {
		t.Fatalf("output gate: parsing fixture: %v", err)
	}
	outputgateAssertFound(t, v, "dot-import:fmt")
}

// TestOutputGate_SeededViolationRed_BarePrintln proves the implicitly-
// stdout fmt.Println case — no os.Stdout identifier appears at all.
func TestOutputGate_SeededViolationRed_BarePrintln(t *testing.T) {
	fixture := filepath.Join(outputgateFixtureDir(t), "bare_println_violation.go")
	v, err := OutputgateScanFile(fixture)
	if err != nil {
		t.Fatalf("output gate: parsing fixture: %v", err)
	}
	outputgateAssertFound(t, v, "fmt.Println")
}

// TestOutputGate_SeededViolationRed_FprintTargetsStdout proves the
// R-14.137 Fprint*-targeting decision: fmt.Fprintln(os.Stdout, msg) is
// caught via the general os.Stdout match on its first argument, with no
// dedicated Fprint*-argument rule.
func TestOutputGate_SeededViolationRed_FprintTargetsStdout(t *testing.T) {
	fixture := filepath.Join(outputgateFixtureDir(t), "fprint_target_violation.go")
	v, err := OutputgateScanFile(fixture)
	if err != nil {
		t.Fatalf("output gate: parsing fixture: %v", err)
	}
	outputgateAssertFound(t, v, "os.Stdout")
}

// TestOutputGate_SeededViolationRed_CrossPackageHelper is the R-14.137
// whole-program proof: crosspkg/helper.go writes to os.Stdout directly and
// must be RED on its own, independent of any caller; crosspkg/caller.go,
// which only calls the helper and names no os/fmt identifier itself, must
// stay GREEN. A cmd/-only scan (the old forbidigo scope) would have seen
// neither file if the helper lived outside cmd/ — this gate catches the
// helper at its own definition site because it scans every non-exempt
// package, not only cmd/.
func TestOutputGate_SeededViolationRed_CrossPackageHelper(t *testing.T) {
	dir := filepath.Join(outputgateFixtureDir(t), "crosspkg")

	helperViolations, err := OutputgateScanFile(filepath.Join(dir, "helper.go"))
	if err != nil {
		t.Fatalf("output gate: parsing helper.go: %v", err)
	}
	outputgateAssertFound(t, helperViolations, "os.Stdout")

	callerViolations, err := OutputgateScanFile(filepath.Join(dir, "caller.go"))
	if err != nil {
		t.Fatalf("output gate: parsing caller.go: %v", err)
	}
	if len(callerViolations) != 0 {
		t.Fatalf("output gate: caller.go should be clean (no direct os/fmt reference), got %+v", callerViolations)
	}
}

// TestOutputGate_FalsePositive_LegitimateWriterUse proves the gate does
// NOT fire on fmt.Fprintln to a real io.Writer, os.Stdin use, or non-print
// fmt helpers like Sprintf.
func TestOutputGate_FalsePositive_LegitimateWriterUse(t *testing.T) {
	fixture := filepath.Join(outputgateFixtureDir(t), "clean_writer_target.go")
	v, err := OutputgateScanFile(fixture)
	if err != nil {
		t.Fatalf("output gate: parsing fixture: %v", err)
	}
	if len(v) != 0 {
		t.Fatalf("output gate: false positive(s) on legitimate writer use: %+v", v)
	}
}
