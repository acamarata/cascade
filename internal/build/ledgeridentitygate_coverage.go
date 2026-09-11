// Package build (this file) holds ledgeridentitygate.go's literal-
// collection walk and CheckLedgerIdentity, its combining entry point.
// Split out under Art.10.3's 300-line cap, matching schemaceilinggate.go's
// own _coverage.go split before R-16.77 retired it.
package build

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// migrationSetLiteral is one migrate.MigrationSet{...} composite literal
// this walk found, plus whatever it could determine about its SetID key.
type migrationSetLiteral struct {
	Line       int
	HasSetID   bool
	SetIDValue string // valid only when Resolvable
	Resolvable bool
}

// findMigrationSetLiterals returns every "<alias>.MigrationSet{...}"
// composite literal in f.
func findMigrationSetLiterals(fset *token.FileSet, f *ast.File, alias string) []migrationSetLiteral {
	var out []migrationSetLiteral
	if alias == "" {
		return out
	}
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := cl.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "MigrationSet" {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || id.Name != alias {
			return true
		}
		out = append(out, literalFromComposite(fset, cl))
		return true
	})
	return out
}

// literalFromComposite inspects one already-matched composite literal's
// fields for its SetID key.
func literalFromComposite(fset *token.FileSet, cl *ast.CompositeLit) migrationSetLiteral {
	lit := migrationSetLiteral{Line: fset.Position(cl.Pos()).Line}
	for _, elt := range cl.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "SetID" {
			continue
		}
		lit.HasSetID = true
		if s, ok := resolveStringLiteral(kv.Value); ok {
			lit.SetIDValue = s
			lit.Resolvable = true
		}
	}
	return lit
}

// claimedSetID records the first package to claim one resolvable SetID
// string, for the duplicate check.
type claimedSetID struct {
	file string
	line int
	dir  string
}

// LedgerIdentitySkipsPath reports whether rel is fixture data the
// REAL-TREE scan must not read — identical rationale to
// CountsDriftSkipsPath and sweep.go's SweepSkipsPath. This gate's own
// seeded-violation fixtures under testdata/ exist precisely to CONTAIN a
// missing and a duplicate SetID, so scanning them against the live tree
// makes the gate permanently red on its own test data. That is not a
// hypothetical: this gate shipped without this filter and its
// RealTreeGreen check failed immediately, reporting the two fixtures it
// is built to detect — the same defect the SPORT inventory gate hit for
// the same reason.
//
// The filter deliberately lives OUT of CheckLedgerIdentity: the seeded
// tests hand that function fixture paths directly and MUST still see the
// violations, or the proof that the gate bites would be silently
// destroyed by the very filter meant to keep the tree green.
func LedgerIdentitySkipsPath(rel string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == "testdata" {
			return true
		}
	}
	return false
}

// CheckLedgerIdentityTracked is the REAL-TREE entry point: it drops
// fixture paths per LedgerIdentitySkipsPath, then defers to
// CheckLedgerIdentity. Callers scanning the live tree (the RealTreeGreen
// check, and any future CI caller) must use THIS function; callers
// proving the gate bites pass their fixture paths to CheckLedgerIdentity
// directly.
func CheckLedgerIdentityTracked(root string, files []string) ([]LedgerIdentityViolation, error) {
	kept := make([]string, 0, len(files))
	for _, rel := range files {
		if LedgerIdentitySkipsPath(rel) {
			continue
		}
		kept = append(kept, rel)
	}
	return CheckLedgerIdentity(root, kept)
}

// CheckLedgerIdentity scans every non-test .go file in files (module-
// relative paths, as ListTrackedFiles returns) for a ledger-identity
// violation: see this package's ledgeridentitygate.go doc comment for
// the full derivation and its stated blind spots. It applies NO path
// filtering — see CheckLedgerIdentityTracked for the real-tree entry
// point.
func CheckLedgerIdentity(root string, files []string) ([]LedgerIdentityViolation, error) {
	sorted := append([]string(nil), files...)
	sort.Strings(sorted)

	var out []LedgerIdentityViolation
	claimed := map[string]claimedSetID{}
	for _, rel := range sorted {
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			continue
		}
		v, err := ledgerIdentityViolationsInFile(root, rel, claimed)
		if err != nil {
			return nil, err
		}
		out = append(out, v...)
	}
	return out, nil
}

// ledgerIdentityViolationsInFile parses one file and returns its
// violations, mutating claimed as it goes (the first package to claim a
// resolvable SetID wins; every later DIFFERENT package claiming the same
// value is a duplicate violation).
func ledgerIdentityViolationsInFile(root, rel string, claimed map[string]claimedSetID) ([]LedgerIdentityViolation, error) {
	full := filepath.Join(root, rel)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, full, nil, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("ledger identity gate: parse %s: %w", rel, err)
	}
	alias := migrateAliasForFile(f)
	if alias == "" {
		return nil, nil
	}
	dir := filepath.Dir(rel)

	var out []LedgerIdentityViolation
	for _, lit := range findMigrationSetLiterals(fset, f, alias) {
		if !lit.HasSetID {
			out = append(out, LedgerIdentityViolation{File: rel, Line: lit.Line, Kind: "missing"})
			continue
		}
		if !lit.Resolvable || lit.SetIDValue == "" {
			continue
		}
		prev, seen := claimed[lit.SetIDValue]
		if !seen {
			claimed[lit.SetIDValue] = claimedSetID{file: rel, line: lit.Line, dir: dir}
			continue
		}
		if prev.dir != dir {
			out = append(out, LedgerIdentityViolation{
				File: rel, Line: lit.Line, Kind: "duplicate", SetID: lit.SetIDValue,
				OtherFile: prev.file, OtherLine: prev.line,
			})
		}
	}
	return out, nil
}
