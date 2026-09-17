package main

// Purpose (this file): every value handed to output.Writer.Result must
//   render for a HUMAN, which means it must implement fmt.Stringer.
//
// WHY THIS IS A GATE AND NOT A CONVENTION. Result does `w.Println(data)`
//   in human mode. A value with no String method therefore prints Go's
//   default formatting, and nobody notices until an operator sees it: the
//   W-4 hardening gate found `cascade backup target list` printing
//   `map[targets:[]]` and `cascade backup list` printing `{[]}`, on the
//   epic whose acceptance drill is recovering a lost laptop — the moment
//   an operator most needs to read the output. R-14.253 Finding 2 ruled on
//   exactly this class once already; a ruling is not a gate.
//
// It is type-aware rather than a grep, because the question ("does this
//   expression's type implement fmt.Stringer?") is a type question and a
//   textual approximation of it would both miss cases and invent them.
// SPORT: cmd/cascade result-stringer gate (ADD) — P1-E19-W4-S42-T7.

import (
	"go/ast"
	"go/types"
	"testing"

	"golang.org/x/tools/go/packages"
)

// TestEveryResultValueRendersForAHuman walks this package's own syntax
// with type information and refuses a Result argument that cannot render
// itself.
func TestEveryResultValueRendersForAHuman(t *testing.T) {
	pkg := loadThisPackage(t)
	stringer := stringerInterface(t, pkg)

	var bad []string
	for i, file := range pkg.Syntax {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Result" || !isOutputWriter(pkg, sel.X) {
				return true
			}
			argType := pkg.TypesInfo.TypeOf(call.Args[0])
			if argType == nil || types.Implements(argType, stringer) ||
				types.Implements(types.NewPointer(argType), stringer) {
				return true
			}
			bad = append(bad, pkg.Fset.Position(call.Pos()).String()+": "+argType.String())
			return true
		})
		_ = i
	}
	if len(bad) == 0 {
		return
	}
	for _, b := range bad {
		t.Logf("  %s", b)
	}
	t.Errorf("%d Result value(s) have no String method, so `cascade <verb>` prints Go's default "+
		"formatting to an operator (R-14.253 Finding 2)", len(bad))
}

// loadThisPackage type-checks cmd/cascade.
func loadThisPackage(t *testing.T) *packages.Package {
	t.Helper()
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedSyntax |
		packages.NeedTypes | packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		t.Fatalf("loading cmd/cascade: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("loaded %d packages, want exactly this one", len(pkgs))
	}
	if len(pkgs[0].Syntax) == 0 {
		t.Fatal("the package loaded with no syntax; this gate would pass having read nothing")
	}
	return pkgs[0]
}

// stringerInterface resolves fmt.Stringer from the loaded program rather
// than reconstructing it, so the gate compares against the real interface.
func stringerInterface(t *testing.T, pkg *packages.Package) *types.Interface {
	t.Helper()
	fmtPkg, ok := pkg.Imports["fmt"]
	if !ok || fmtPkg.Types == nil {
		t.Fatal("fmt is not among this package's resolved imports")
	}
	obj := fmtPkg.Types.Scope().Lookup("Stringer")
	if obj == nil {
		t.Fatal("fmt.Stringer did not resolve")
	}
	iface, ok := obj.Type().Underlying().(*types.Interface)
	if !ok {
		t.Fatalf("fmt.Stringer resolved to %T, want an interface", obj.Type().Underlying())
	}
	return iface
}

// isOutputWriter reports whether expr's type is *internal/output.Writer.
//
// Checked by TYPE rather than by the receiver's spelling: this package
// has a dozen per-noun helpers (vaultOutputWriter, nodeOutputWriter,
// backupOutputWriter…) and a name-based match would have to know all of
// them, and would silently stop covering the next one.
func isOutputWriter(pkg *packages.Package, expr ast.Expr) bool {
	t := pkg.TypesInfo.TypeOf(expr)
	if t == nil {
		return false
	}
	ptr, ok := t.(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := ptr.Elem().(*types.Named)
	if !ok {
		return false
	}
	return named.Obj().Name() == "Writer" &&
		named.Obj().Pkg() != nil &&
		named.Obj().Pkg().Path() == "github.com/acamarata/cascade/internal/output"
}

// TestTheGateWouldCatchANonStringer is the mutation proof: the predicate
// above must actually reject something, or a green run means nothing.
func TestTheGateWouldCatchANonStringer(t *testing.T) {
	pkg := loadThisPackage(t)
	stringer := stringerInterface(t, pkg)

	// A map — the exact shape the gate was written for.
	mapType := types.NewMap(types.Typ[types.String], types.NewInterfaceType(nil, nil))
	if types.Implements(mapType, stringer) {
		t.Error("a map[string]any reports as a Stringer; the gate accepts everything")
	}
	// And something that really is one still passes.
	obj := pkg.Types.Scope().Lookup("syncStatusView")
	if obj == nil {
		t.Skip("syncStatusView is gone; pick another view for this half of the proof")
	}
	if !types.Implements(obj.Type(), stringer) && !types.Implements(types.NewPointer(obj.Type()), stringer) {
		t.Error("syncStatusView does not report as a Stringer; the gate rejects everything")
	}
}
