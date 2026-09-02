// This file holds the boundary lint's seeded-violation (failing-case) tests
// — split out of boundary_test.go under R-14.117 to keep both files under
// Art.10.3's 300-line cap, authorized by R-14.120's hardening pass. The
// scanning logic itself (scanBoundary, scanFile, boundaryResolveImports)
// stays in boundary_test.go; this file only exercises it against the
// fixtures under testdata/seeded-violations/boundary/.
package build

import (
	"path/filepath"
	"testing"
)

// boundarySeededFixtureDir returns the seeded-violation fixture root shared
// by every subtest in this file.
func boundarySeededFixtureDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(moduleRoot(t), "internal", "build", "testdata", "seeded-violations", "boundary")
}

// TestBoundaryLint_SeededViolation is the original failing case (AC: "fails
// on its seeded raw-error boundary fixture"): violation.go deliberately
// contains both original raw-constructor forms, and the scanner must find
// both.
func TestBoundaryLint_SeededViolation(t *testing.T) {
	fixture := boundarySeededFixtureDir(t)
	violations := scanBoundary(t, fixture)
	if len(violations) == 0 {
		t.Fatalf("boundary lint: expected violations in seeded fixture %s, found none", fixture)
	}

	var sawErrorf, sawErrorsNew bool
	for _, v := range violations {
		switch v.call {
		case "fmt.Errorf":
			sawErrorf = true
		case "errors.New":
			sawErrorsNew = true
		}
	}
	if !sawErrorf {
		t.Error("seeded fixture: expected at least one fmt.Errorf violation, found none")
	}
	if !sawErrorsNew {
		t.Error("seeded fixture: expected at least one errors.New violation, found none")
	}
}

// TestBoundaryLint_SeededViolation_ImportAlias proves EVASION-1's alias half
// (R-14.120(a)): alias_violation.go imports "fmt" under the alias "ferrors"
// and calls ferrors.Errorf — a literal `fmt.Errorf` selector match would
// miss this entirely, since the selector's package identifier is "ferrors",
// not "fmt". The gate must still resolve it to fmt.Errorf via the file's
// own import declaration and flag it as a real fixture the scanner walks,
// not a string the test constructs itself.
func TestBoundaryLint_SeededViolation_ImportAlias(t *testing.T) {
	fixture := boundarySeededFixtureDir(t)
	violations := scanBoundary(t, fixture)

	var found bool
	for _, v := range violations {
		if v.call == "fmt.Errorf" && filepath.Base(v.file) == "alias_violation.go" {
			found = true
		}
	}
	if !found {
		t.Error("seeded fixture: expected fmt.Errorf found via aliased import (ferrors.Errorf) in alias_violation.go, found none")
	}
}

// TestBoundaryLint_SeededViolation_DotImport proves EVASION-1's dot-import
// half (R-14.120(a)): dotimport_violation.go dot-imports "errors" and calls
// New(...) unqualified, which is not even a *ast.SelectorExpr and so cannot
// be caught by selector matching at all. The gate must instead reject the
// dot-import declaration itself, outright, independent of how the
// dot-imported names are used.
func TestBoundaryLint_SeededViolation_DotImport(t *testing.T) {
	fixture := boundarySeededFixtureDir(t)
	violations := scanBoundary(t, fixture)

	var found bool
	for _, v := range violations {
		if v.call == "dot-import:errors" && filepath.Base(v.file) == "dotimport_violation.go" {
			found = true
		}
	}
	if !found {
		t.Error("seeded fixture: expected dot-import:errors violation in dotimport_violation.go, found none")
	}
}

// TestBoundaryLint_SeededViolation_ErrorsJoin proves EVASION-2
// (R-14.120(b)): errors.Join mints a raw, kindless error exactly like
// errors.New and fmt.Errorf but was absent from the original denied set.
// join_violation.go calls it directly at a scanned boundary.
func TestBoundaryLint_SeededViolation_ErrorsJoin(t *testing.T) {
	fixture := boundarySeededFixtureDir(t)
	violations := scanBoundary(t, fixture)

	var found bool
	for _, v := range violations {
		if v.call == "errors.Join" && filepath.Base(v.file) == "join_violation.go" {
			found = true
		}
	}
	if !found {
		t.Error("seeded fixture: expected errors.Join violation in join_violation.go, found none")
	}
}
