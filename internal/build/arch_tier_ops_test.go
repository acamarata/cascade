package build

// Purpose: reject tier arithmetic and implicit database serialization.
// Inputs: typed production and test syntax for every supported operating system.
// Outputs: unique file:line findings, including alias and pointer operands.
// Constraints: only the canonical tier implementation and codec are exempt.
// SPORT: sensitivity boundary enforcement.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func tierOperand(typ types.Type) bool {
	if typ == nil {
		return false
	}
	typ = types.Unalias(typ)
	if ptr, ok := typ.(*types.Pointer); ok {
		return tierOperand(ptr.Elem())
	}
	named, ok := typ.(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "github.com/acamarata/cascade/pkg/provider" && named.Obj().Name() == "SensitivityTier"
}

func tierCallArgument(call *ast.CallExpr, info *types.Info) bool {
	for _, arg := range call.Args {
		if tierOperand(info.TypeOf(arg)) {
			return true
		}
	}
	return false
}

func tierUnsafeCall(call *ast.CallExpr, info *types.Info) bool {
	if !tierCallArgument(call, info) {
		return false
	}
	fun := ast.Unparen(call.Fun)
	if indexed, ok := fun.(*ast.IndexExpr); ok {
		fun = ast.Unparen(indexed.X)
	}
	switch fun := fun.(type) {
	case *ast.Ident:
		builtin, ok := info.Uses[fun].(*types.Builtin)
		return ok && (builtin.Name() == "min" || builtin.Name() == "max")
	case *ast.SelectorExpr:
		if selection := info.Selections[fun]; selection != nil {
			name := strings.TrimSuffix(selection.Obj().Name(), "Context")
			return name == "Exec" || strings.HasPrefix(name, "Query")
		}
		obj := info.Uses[fun.Sel]
		return obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == "cmp" && (obj.Name() == "Compare" || obj.Name() == "Less")
	}
	return false
}

// tierNumericOperators lists the ordered and arithmetic binary operators the
// gate refuses on a tier operand. A set lookup, not a switch, keeps the
// exhaustive linter off the open-ended token.Token enumeration.
var tierNumericOperators = []token.Token{
	token.LSS, token.GTR, token.LEQ, token.GEQ,
	token.ADD, token.SUB, token.MUL, token.QUO, token.REM,
	token.AND, token.OR, token.XOR, token.SHL, token.SHR, token.AND_NOT,
}

// tierTermConstraint reports whether a type-parameter constraint carries type
// terms (a union, ~T or a concrete type, directly or through an embedded
// interface). Such a constraint admits ordered or arithmetic operators in the
// generic body; comparable and method-only constraints do not.
func tierTermConstraint(constraint types.Type) bool {
	iface, ok := constraint.Underlying().(*types.Interface)
	if !ok {
		return true
	}
	for i := range iface.NumEmbeddeds() {
		embedded := iface.EmbeddedType(i)
		if _, nested := embedded.Underlying().(*types.Interface); !nested || tierTermConstraint(embedded) {
			return true
		}
	}
	return false
}

// tierGenericInstance reports an instantiation, explicit or inferred, that
// passes the tier as a type argument for a type parameter whose constraint has
// type terms: the generic body could order or convert the tier unseen. When
// the type parameters cannot be resolved, any tier type argument is reported.
func tierGenericInstance(id *ast.Ident, info *types.Info) bool {
	inst, ok := info.Instances[id]
	if !ok {
		return false
	}
	var params *types.TypeParamList
	switch obj := info.Uses[id].(type) {
	case *types.Func:
		params = obj.Origin().Signature().TypeParams()
	case *types.TypeName:
		switch generic := obj.Type().(type) {
		case *types.Named:
			params = generic.Origin().TypeParams()
		case *types.Alias:
			params = generic.Origin().TypeParams()
		}
	}
	for i := range inst.TypeArgs.Len() {
		resolved := params != nil && i < params.Len()
		if tierOperand(inst.TypeArgs.At(i)) && (!resolved || tierTermConstraint(params.At(i).Constraint())) {
			return true
		}
	}
	return false
}

func tierNumericOperator(op token.Token) bool {
	return slices.Contains(tierNumericOperators, op)
}

func tierViolation(n ast.Node, info *types.Info) bool {
	switch n := n.(type) {
	case *ast.Ident:
		return tierGenericInstance(n, info)
	case *ast.BinaryExpr:
		return tierNumericOperator(n.Op) && (tierOperand(info.TypeOf(n.X)) || tierOperand(info.TypeOf(n.Y)))
	case *ast.UnaryExpr:
		return (n.Op == token.ADD || n.Op == token.SUB || n.Op == token.XOR) && tierOperand(info.TypeOf(n.X))
	case *ast.IndexExpr:
		return !info.Types[n.Index].IsType() && tierOperand(info.TypeOf(n.Index))
	case *ast.IncDecStmt:
		return tierOperand(info.TypeOf(n.X))
	case *ast.AssignStmt:
		if n.Tok != token.ASSIGN && n.Tok != token.DEFINE {
			for _, expr := range append(append([]ast.Expr{}, n.Lhs...), n.Rhs...) {
				if tierOperand(info.TypeOf(expr)) {
					return true
				}
			}
		}
	case *ast.CallExpr:
		if len(n.Args) == 1 && info.Types[n.Fun].IsType() && tierOperand(info.TypeOf(n.Args[0])) && !tierOperand(info.TypeOf(n.Fun)) {
			return true
		}
		return tierUnsafeCall(n, info)
	}
	return false
}

func tierFindings(pkgs []*packages.Package, root string) []string {
	found := map[string]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			path := pkg.Fset.Position(file.Pos()).Filename
			rel, err := filepath.Rel(root, path)
			if err != nil {
				rel = path
			}
			rel = filepath.ToSlash(rel)
			if rel == "pkg/provider/sensitivity.go" || rel == "pkg/provider/sensitivity_dataclass.go" {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				if tierViolation(n, pkg.TypesInfo) {
					found[fmt.Sprintf("%s:%d", rel, pkg.Fset.Position(n.Pos()).Line)] = true
				}
				return true
			})
		}
	}
	out := make([]string, 0, len(found))
	for finding := range found {
		out = append(out, finding)
	}
	sort.Strings(out)
	return out
}

func loadTierPackages(t *testing.T, root, goos, pattern string) []*packages.Package {
	t.Helper()
	env := append(os.Environ(), "GOOS="+goos, "GOARCH=amd64", "CGO_ENABLED=0", "GOPROXY=off", "GOSUMDB=off")
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:  root, Env: env, BuildFlags: []string{"-tags=devkeys"}, Tests: true,
	}, pattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) == 0 {
		t.Fatal("tier gate loaded no packages")
	}
	for _, pkg := range pkgs {
		if len(pkg.Errors) != 0 {
			t.Fatalf("%s: %v", pkg.PkgPath, pkg.Errors)
		}
	}
	return pkgs
}

func TestArchTierOps_RealTreeGreen(t *testing.T) {
	root := archModuleRoot(t)
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			pkgs := loadTierPackages(t, root, goos, "./...")
			if got := tierFindings(pkgs, root); len(got) != 0 {
				t.Fatalf("tier operations:\n%s", strings.Join(got, "\n"))
			}
		})
	}
}

func TestArchTierOps_SeededViolationRed(t *testing.T) {
	root := archModuleRoot(t)
	pkgs := loadTierPackages(t, root, "linux", "./internal/build/testdata/seeded-violations/tier-ops")
	got := tierFindings(pkgs, root)
	var want []string
	for _, name := range []string{"generics.go", "violations.go", "violations_test.go"} {
		path := "internal/build/testdata/seeded-violations/tier-ops/" + name
		body, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			if strings.Contains(line, "// want tier violation") {
				want = append(want, fmt.Sprintf("%s:%d", path, i+1))
			}
		}
	}
	sort.Strings(want)
	if len(want) != 52 {
		t.Fatalf("fixture has %d cases, want 52", len(want))
	}
	for _, finding := range got {
		t.Log(finding)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("findings = %v; want exactly %v", got, want)
	}
}
