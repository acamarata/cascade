// Purpose: the construction precondition of the provider-decorator
//
//	exemption (P1-BF-R106). seam_decorators_test.go exempts the forwarding
//	methods of a listed decorator type; that is only sound while the type
//	is built solely inside Resolver.build of internal/providers/dispatch/
//	build.go. seamConstructions reports every other construction (composite
//	literal, new(T), zero-value var T, any func with a T or *T result), and
//	one test asserts the real module has none, so the exemption cannot stay
//	in force once an outside construction exists. Helpers and the seed type
//	are shared in-package with seam_decorators_test.go.
package conductor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	seamDispatchDir = "internal/providers/dispatch/"
	seamBuildFile   = seamDispatchDir + "build.go"
)

// seamParse parses src (nil: read name from disk), failing the test on error.
func seamParse(t *testing.T, name string, src any) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}
	return fset, file
}

// seamConstructions returns where file, presented as repo file rel,
// constructs a listed decorator type outside Resolver.build of seamBuildFile
// (R106): a composite literal, new(T), a var of type T (zero value) or a func
// (decl or literal) with a T or *T result. A constructor is reported where it
// is declared, so its callers need no rule of their own.
func seamConstructions(file *ast.File, rel string) (out []token.Pos) {
	isT := func(e ast.Expr) bool {
		if s, ok := e.(*ast.StarExpr); ok {
			e = s.X
		}
		id, ok := e.(*ast.Ident)
		return ok && slices.ContainsFunc(seamDecorators, func(d seamDecorator) bool { return d.recv == id.Name })
	}
	add := func(n ast.Node, e ast.Expr) {
		if isT(e) {
			out = append(out, n.Pos())
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			_, recv := seamRecvNames(x)
			return filepath.ToSlash(rel) != seamBuildFile || recv != "Resolver" || x.Name.Name != "build"
		case *ast.CompositeLit:
			add(x, x.Type)
		case *ast.ValueSpec:
			add(x, x.Type)
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "new" && len(x.Args) == 1 {
				add(x, x.Args[0])
			}
		case *ast.FuncType:
			for _, f := range seamResults(x) {
				add(f, f.Type)
			}
		}
		return true
	})
	return out
}

// seamResults lists the result fields of ft (none when it has no results).
func seamResults(ft *ast.FuncType) []*ast.Field {
	if ft.Results == nil {
		return nil
	}
	return ft.Results.List
}

// TestSeamDecorator_ConstructionStaysInBuild is the exemption's precondition
// (R106): across the whole module, no non-test file constructs a listed
// decorator outside Resolver.build. It is its own failing assertion, so the
// exemption cannot stay in force once an outside construction exists.
func TestSeamDecorator_ConstructionStaysInBuild(t *testing.T) {
	root, bad := seamModuleRoot(t), []string(nil)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if seamSkipsDir(d.Name()) || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			rel, _ := filepath.Rel(root, path)
			fset, file := seamParse(t, path, nil)
			for _, pos := range seamConstructions(file, rel) {
				bad = append(bad, fset.Position(pos).String())
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module tree: %v", err)
	}
	if len(bad) > 0 {
		t.Errorf("decorator constructed outside Resolver.build: %v", bad)
	}
}

// TestSeamDecorator_ConstructionSeedsAreCaught seeds every outside
// construction shape (the rule must report each) and checks the real
// build.go construction is allowed only at its real place.
func TestSeamDecorator_ConstructionSeedsAreCaught(t *testing.T) {
	h, o := seamDecoratorHead, seamDispatchDir+"other.go"
	lit := "&laneOutcomeProvider{inner: mp}"
	use := h + "\nfunc use(mp provider.ModelProvider, ctx, req any) { (" + lit + ").Chat(ctx, req) }\n"
	build := func(recv, name string) string {
		return h + "\ntype Resolver struct{}\n\nfunc (r *" + recv + ") " + name + "(mp provider.ModelProvider) any { return " + lit + " }\n"
	}
	realBuild, err := os.ReadFile(filepath.Join(seamModuleRoot(t), filepath.FromSlash(seamBuildFile)))
	if err != nil {
		t.Fatalf("reading the real build.go: %v", err)
	}
	seeds := []seamSeed{
		{"composite literal then Chat", o, use, 1},
		{"composite literal in the listed file", seamDecoratorFile, use, 1},
		{"new outside build", o, h + "\nfunc use() { _ = new(laneOutcomeProvider) }\n", 1},
		{"zero-value var", o, h + "\nfunc use() { var p laneOutcomeProvider; _ = p }\n", 1},
		{"constructor func", o, h + "\nfunc newLane(mp provider.ModelProvider) *laneOutcomeProvider { return " + lit + " }\n", 2},
		{"constructor literal", o, h + "\nvar mk = func() *laneOutcomeProvider { return nil }\n", 1},
		{"build in another file", o, build("Resolver", "build"), 1},
		{"another Resolver method in build.go", seamBuildFile, build("Resolver", "rebuild"), 1},
		{"build on another receiver in build.go", seamBuildFile, build("other", "build"), 1},
		{"Resolver.build in build.go is allowed", seamBuildFile, build("Resolver", "build"), 0},
		{"the real build.go is allowed", seamBuildFile, string(realBuild), 0},
		{"the real build.go anywhere else is caught", o, string(realBuild), 1},
	}
	for _, tc := range seeds {
		t.Run(tc.name, func(t *testing.T) {
			_, file := seamParse(t, tc.rel, tc.src)
			if got := len(seamConstructions(file, tc.rel)); got != tc.want {
				t.Fatalf("construction rule reports %d, want %d", got, tc.want)
			}
		})
	}
}
