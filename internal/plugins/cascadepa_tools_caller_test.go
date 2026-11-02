package plugins

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const cpaModule = "github.com/acamarata/cascade/"

func TestCPAToolAdaptersUseModelFacingClient(t *testing.T) {
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports | packages.NeedDeps | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo}
	pkgs, err := packages.Load(cfg, "../../internal/...", "../../cmd/...", "../../pkg/...", "../../plugins/...")
	if err != nil {
		t.Fatal(err)
	}
	var adapters map[string]bool
	modelCalls := 0
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if len(p.Errors) != 0 {
			t.Fatal(p.Errors)
		}
		if p.PkgPath == cpaModule+"internal/plugins" {
			adapters = toolAdapterFiles(p)
			for _, violation := range adapterViolations(p, adapters) {
				t.Error(violation)
			}
			assertSeededClientRejected(t, p, adapters)
		}
		for _, file := range p.Syntax {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !clientCall(p, call, "ModelFacing") {
					return true
				}
				modelCalls++
				if filepath.Base(p.Fset.Position(call.Pos()).Filename) != "cascadepa_caller.go" {
					t.Error("ModelFacing outside caller helper")
				}
				return true
			})
		}
	})
	if modelCalls != 1 {
		t.Fatalf("ModelFacing calls = %d", modelCalls)
	}
	assertAdapterSet(t, adapters)
}

func assertAdapterSet(t *testing.T, adapters map[string]bool) {
	t.Helper()
	for _, name := range []string{"cascadepa_tools_wiring.go", "cascadepa_bridge_chatsend_client.go"} {
		if !adapters[name] {
			t.Errorf("adapter discovery missed %s", name)
		}
	}
	for _, name := range []string{"cascadepa_wiring.go", "cascadepa_soul_wiring.go", "cascadepa_review_wiring.go", "cascadepa_topics_wiring.go"} {
		if adapters[name] {
			t.Errorf("non-tool adapter matched: %s", name)
		}
	}
}

func clientCall(p *packages.Package, call *ast.CallExpr, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	obj, ok := p.TypesInfo.Uses[sel.Sel].(*types.Func)
	return ok && obj.Pkg() != nil && obj.Pkg().Path() == cpaModule+"internal/client" && obj.Name() == name
}

func toolAdapterFiles(p *packages.Package) map[string]bool {
	initializers := map[types.Object]ast.Expr{}
	for _, f := range p.Syntax {
		ast.Inspect(f, func(n ast.Node) bool {
			v, ok := n.(*ast.ValueSpec)
			if ok && len(v.Names) == len(v.Values) {
				for i, name := range v.Names {
					initializers[p.TypesInfo.Defs[name]] = v.Values[i]
				}
			}
			return true
		})
	}
	matched := map[string]bool{}
	for _, f := range p.Syntax {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			fn, ok := p.TypesInfo.Uses[sel.Sel].(*types.Func)
			if !ok || fn.Pkg() == nil || fn.Pkg().Path() != cpaModule+"plugins/cascade-pa" || !strings.HasPrefix(fn.Name(), "Set") {
				return true
			}
			sig := fn.Type().(*types.Signature)
			if sig.Params().Len() != 1 {
				return true
			}
			typ, ok := sig.Params().At(0).Type().(*types.Named)
			if ok && typ.Obj().Pkg().Path() == cpaModule+"plugins/cascade-pa/tools" {
				for _, arg := range call.Args {
					collectAdapter(p, arg, initializers, matched, map[ast.Expr]bool{})
				}
			}
			return true
		})
	}
	return matched
}

func collectAdapter(p *packages.Package, expr ast.Expr, initializers map[types.Object]ast.Expr, matched map[string]bool, seen map[ast.Expr]bool) {
	if expr == nil || seen[expr] {
		return
	}
	seen[expr] = true
	typ := p.TypesInfo.TypeOf(expr)
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	if named, ok := typ.(*types.Named); ok && named.Obj().Pkg() == p.Types {
		matched[filepath.Base(p.Fset.Position(named.Obj().Pos()).Filename)] = true
	}
	switch e := expr.(type) {
	case *ast.Ident:
		collectAdapter(p, initializers[p.TypesInfo.Uses[e]], initializers, matched, seen)
	case *ast.CallExpr:
		for _, arg := range e.Args {
			collectAdapter(p, arg, initializers, matched, seen)
		}
	}
}

func adapterViolations(p *packages.Package, matched map[string]bool) []string {
	var out []string
	for _, f := range p.Syntax {
		name := filepath.Base(p.Fset.Position(f.Pos()).Filename)
		if !matched[name] {
			continue
		}
		helper := false
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if clientCall(p, call, "New") {
				out = append(out, name+": client.New bypasses caller helper")
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "modelFacingClient" {
				helper = true
			}
			return true
		})
		if !helper {
			out = append(out, name+": no modelFacingClient call")
		}
	}
	return out
}

func assertSeededClientRejected(t *testing.T, p *packages.Package, matched map[string]bool) {
	t.Helper()
	for _, f := range p.Syntax {
		path := p.Fset.Position(f.Pos()).Filename
		if filepath.Base(path) != "cascadepa_tools_wiring.go" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		seed := strings.Replace(string(raw), "modelFacingClient(paths, c.dial, c.timeout)", "client.New(paths.SocketPath(), c.dial, c.timeout)", 1)
		if seed == string(raw) {
			t.Fatal("wiring seed did not change source")
		}
		copyPath := filepath.Join(t.TempDir(), filepath.Base(path))
		if err := os.WriteFile(copyPath, []byte(seed), 0600); err != nil {
			t.Fatal(err)
		}
		seedBytes, err := os.ReadFile(copyPath)
		if err != nil {
			t.Fatal(err)
		}
		cfg := &packages.Config{Mode: packages.LoadAllSyntax, Dir: filepath.Dir(path), Overlay: map[string][]byte{path: seedBytes}, ParseFile: func(fset *token.FileSet, name string, src []byte) (*ast.File, error) {
			return parser.ParseFile(fset, name, src, parser.ParseComments)
		}}
		seeded, err := packages.Load(cfg, ".")
		if err != nil || len(seeded) != 1 {
			t.Fatal("load seeded wiring", err)
		}
		if len(seeded[0].Errors) != 0 {
			t.Fatal(seeded[0].Errors)
		}
		if len(adapterViolations(seeded[0], matched)) == 0 {
			t.Fatal("seeded client.New escaped the adapter gate")
		}
		return
	}
	t.Fatal("no wiring file to seed")
}
