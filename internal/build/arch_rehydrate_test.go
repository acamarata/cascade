// Package build (this file): the executor-only gate for secret rehydration.
// Rule 1: an exported *secrets.Rehydrator method (RehydrateOnly is the only
// one) is reached only from internal/secrets and the conductor executor; the
// production hook caller is added by P1-SUP-02. The check is typed (go/types,
// no substring match): a call, a method value or expression (promoted through
// embedding too), an interface declaring RehydrateOnly, the name in a string
// constant and a conversion to an interface all count. Rule 2: internalGet
// stays inside internal/secrets.
package build

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// rehydrateCallerAllowlist is the set of module-relative paths (a
// directory, or one file) whose files may reach RehydrateOnly.
var rehydrateCallerAllowlist = []string{"internal/secrets", "internal/conductor/executor"}

// rehydrateCallerAllowed matches the slash-separated rel by whole segments.
func rehydrateCallerAllowed(rel string) bool {
	return slices.ContainsFunc(rehydrateCallerAllowlist, func(a string) bool { return rel == a || strings.HasPrefix(rel, a+"/") })
}

// rehydrateReferences lists "file:line: what" for each off-allowlist reference.
func rehydrateReferences(root, goos string, patterns ...string) ([]string, error) {
	pkgs, err := packages.Load(&packages.Config{Dir: root, Env: append(os.Environ(), "GOOS="+goos, "GOARCH=amd64", "CGO_ENABLED=0"),
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo}, patterns...)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, pkg := range pkgs {
		if len(pkg.Errors) != 0 {
			return nil, fmt.Errorf("%s: %v", pkg.PkgPath, pkg.Errors[0])
		}
		for _, file := range pkg.Syntax {
			rel, _ := filepath.Rel(root, pkg.Fset.Position(file.Pos()).Filename)
			if rel = filepath.ToSlash(rel); rehydrateCallerAllowed(rel) {
				continue
			}
			ast.PreorderStack(file, nil, func(n ast.Node, stack []ast.Node) bool {
				if what := rehydrateUse(pkg.TypesInfo, n, stack); what != "" {
					out = append(out, fmt.Sprintf("%s:%d: %s", rel, pkg.Fset.Position(n.Pos()).Line, what))
				}
				return true
			})
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// rehydrateUse names the reference n makes to the guard, or "".
func rehydrateUse(info *types.Info, n ast.Node, stack []ast.Node) string {
	if e, ok := n.(ast.Expr); ok && len(stack) > 0 && !info.Types[e].IsType() && isRehydrator(info.TypeOf(e)) {
		if t := conversionTarget(info, e, stack); t != nil && types.IsInterface(t) {
			return "converts a *secrets.Rehydrator to an interface"
		}
	}
	switch x := n.(type) {
	case *ast.SelectorExpr:
		if sel := info.Selections[x]; sel != nil && x.Sel.IsExported() && rehydratorMethod(sel.Obj()) {
			return "selects " + x.Sel.Name
		}
	case *ast.InterfaceType:
		for _, m := range x.Methods.List {
			if slices.ContainsFunc(m.Names, func(id *ast.Ident) bool { return id.Name == "RehydrateOnly" }) {
				return "declares an interface with RehydrateOnly"
			}
		}
	case *ast.BasicLit:
		if tv := info.Types[x]; tv.Value != nil && tv.Value.Kind() == constant.String && strings.Contains(constant.StringVal(tv.Value), "RehydrateOnly") {
			return "names RehydrateOnly in a string"
		}
	}
	return ""
}

// rehydratorMethod reports a method declared on *secrets.Rehydrator, however
// it is reached (a promoted method's object is still the Rehydrator's).
func rehydratorMethod(obj types.Object) bool {
	fn, ok := obj.(*types.Func)
	if !ok {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	return ok && sig.Recv() != nil && isRehydrator(sig.Recv().Type())
}

// isRehydrator reports the guard type, or a pointer to it, in internal/secrets.
func isRehydrator(t types.Type) bool {
	if p, ok := types.Unalias(t).(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := types.Unalias(t).(*types.Named)
	return ok && n.Obj().Name() == "Rehydrator" && n.Obj().Pkg() != nil && strings.HasSuffix(n.Obj().Pkg().Path(), "internal/secrets")
}

// conversionTarget is the type e's parent assigns it to (a conversion, call
// argument, assignment, var, return, send or literal element), or nil.
func conversionTarget(info *types.Info, e ast.Expr, stack []ast.Node) types.Type {
	switch p := stack[len(stack)-1].(type) {
	case *ast.CallExpr:
		i := slices.Index(p.Args, e)
		if tv := info.Types[p.Fun]; i >= 0 && tv.IsType() {
			return tv.Type
		}
		if sig, ok := info.TypeOf(p.Fun).(*types.Signature); ok && i >= 0 {
			if sig.Variadic() && i >= sig.Params().Len()-1 {
				return sig.Params().At(sig.Params().Len() - 1).Type().(*types.Slice).Elem()
			}
			return sig.Params().At(i).Type()
		}
	case *ast.AssignStmt:
		if i := slices.Index(p.Rhs, e); i >= 0 && len(p.Lhs) == len(p.Rhs) {
			return info.TypeOf(p.Lhs[i])
		}
	case *ast.ValueSpec:
		if p.Type != nil && slices.Contains(p.Values, e) {
			return info.TypeOf(p.Type)
		}
	case *ast.ReturnStmt:
		return resultType(info, slices.Index(p.Results, e), stack)
	case *ast.SendStmt:
		if ch, ok := info.TypeOf(p.Chan).Underlying().(*types.Chan); ok && p.Value == e {
			return ch.Elem()
		}
	case *ast.CompositeLit, *ast.KeyValueExpr:
		return elementType(info, e, stack)
	}
	return nil
}

// resultType is the i-th result of the innermost enclosing function.
func resultType(info *types.Info, i int, stack []ast.Node) types.Type {
	for j := len(stack) - 1; j >= 0 && i >= 0; j-- {
		var fn types.Type
		switch f := stack[j].(type) {
		case *ast.FuncLit:
			fn = info.TypeOf(f)
		case *ast.FuncDecl:
			fn = info.TypeOf(f.Name)
		default:
			continue
		}
		if sig, ok := fn.(*types.Signature); ok && i < sig.Results().Len() {
			return sig.Results().At(i).Type()
		}
		return nil
	}
	return nil
}

// elementType is the element or struct field type a composite literal gives e.
func elementType(info *types.Info, e ast.Expr, stack []ast.Node) types.Type {
	lit, field := ast.Node(nil), ""
	for j := len(stack) - 1; j >= 0 && lit == nil; j-- {
		if kv, ok := stack[j].(*ast.KeyValueExpr); ok && kv.Value == e {
			field = fmt.Sprint(kv.Key)
		} else if cl, ok := stack[j].(*ast.CompositeLit); ok {
			lit = cl
		}
	}
	if lit == nil {
		return nil
	}
	switch u := info.TypeOf(lit.(ast.Expr)).Underlying().(type) {
	case interface{ Elem() types.Type }:
		return u.Elem()
	case *types.Struct:
		for i := range u.NumFields() {
			if u.Field(i).Name() == field {
				return u.Field(i).Type()
			}
		}
	}
	return nil
}

// TestArchRehydrateCallers asserts rule 1 on the real tree, for every GOOS.
func TestArchRehydrateCallers(t *testing.T) {
	root := archModuleRoot(t)
	for _, goos := range []string{"linux", "darwin", "windows"} {
		got, err := rehydrateReferences(root, goos, "./cmd/...", "./internal/...", "./pkg/...", "./plugins/...")
		if err != nil {
			t.Fatalf("%s: %v", goos, err)
		}
		if len(got) != 0 {
			t.Fatalf("%s: RehydrateOnly reached outside %v:\n%s", goos, rehydrateCallerAllowlist, strings.Join(got, "\n"))
		}
	}
}

const fixtureHead = "package %s\n\nimport (\n\t\"context\"\n\n\t\"example.com/fixture/internal/secrets\"\n)\n\nvar _ = context.Background\n\n"

// seededReferences returns the references in a fixture module of files.
func seededReferences(t *testing.T, files map[string]string) []string {
	t.Helper()
	root := t.TempDir()
	files["go.mod"] = "module example.com/fixture\n\ngo 1.23\n"
	files["internal/secrets/secrets.go"] = "package secrets\n\nimport \"context\"\n\ntype Tag struct{ Name string }\n\n" +
		"type Rehydrator struct{}\n\nfunc (r *Rehydrator) RehydrateOnly(context.Context, []byte, func(Tag) bool) ([]byte, error) { return nil, nil }\n"
	writeAllFiles(t, root, files)
	got, err := rehydrateReferences(root, "linux", "./...")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestArchRehydrateCallersDetectsRehydrateOnly: an off-allowlist call (a
// lookalike prefix and a promoted call through embedding too) is red; the
// allowlisted packages and a concrete hand-off are not.
func TestArchRehydrateCallersDetectsRehydrateOnly(t *testing.T) {
	call := "func Run(r *secrets.Rehydrator) { _, _ = r.RehydrateOnly(context.Background(), nil, nil) }\n"
	got := seededReferences(t, map[string]string{
		"internal/plugins/call.go":           fmt.Sprintf(fixtureHead, "plugins") + call,
		"internal/hooks/binding.go":          fmt.Sprintf(fixtureHead, "hooks") + call,
		"internal/hooks/other.go":            fmt.Sprintf(fixtureHead, "hooks") + "func other(r *secrets.Rehydrator) { Run(r) }\n",
		"internal/secretsish/call.go":        fmt.Sprintf(fixtureHead, "secretsish") + call,
		"internal/secrets/own.go":            "package secrets\n\nimport \"context\"\n\nfunc own(r *Rehydrator) { _, _ = r.RehydrateOnly(context.Background(), nil, nil) }\n",
		"internal/conductor/executor/run.go": fmt.Sprintf(fixtureHead, "executor") + call,
		"internal/wrap/wrap.go": fmt.Sprintf(fixtureHead, "wrap") + "type W struct{ *secrets.Rehydrator }\n\n" +
			"func Use(w W) { _, _ = w.RehydrateOnly(context.Background(), nil, nil) }\n",
		"cmd/app/main.go": "package main\n\nimport (\n\t\"example.com/fixture/internal/hooks\"\n\t\"example.com/fixture/internal/secrets\"\n)\n\n" +
			"func main() { hooks.Run(&secrets.Rehydrator{}) }\n",
	})
	want := []string{"internal/hooks/binding.go:11: selects RehydrateOnly", "internal/plugins/call.go:11: selects RehydrateOnly",
		"internal/secretsish/call.go:11: selects RehydrateOnly", "internal/wrap/wrap.go:13: selects RehydrateOnly"}
	if !slices.Equal(got, want) {
		t.Fatalf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestArchRehydrateMethodValueDetected: method values and expressions, an
// interface, a reflective name and each interface conversion are references.
func TestArchRehydrateMethodValueDetected(t *testing.T) {
	body := fmt.Sprintf(fixtureHead, "plugins") + `type guard interface{ RehydrateOnly(context.Context, []byte, func(secrets.Tag) bool) ([]byte, error) }

func shapes(r *secrets.Rehydrator, ch chan any) any {
	f := r.RehydrateOnly
	_ = f
	_ = (*secrets.Rehydrator).RehydrateOnly
	var _ any = r
	_ = []any{r}
	_ = struct{ V any }{V: r}
	ch <- r
	take(r)
	_ = "RehydrateOnly"
	return r
}

func take(...any) {}
`
	got := seededReferences(t, map[string]string{"internal/plugins/value.go": body})
	const v, conv = "internal/plugins/value.go:", ": converts a *secrets.Rehydrator to an interface"
	want := []string{v + "11: declares an interface with RehydrateOnly", v + "14: selects RehydrateOnly", v + "16: selects RehydrateOnly",
		v + "17" + conv, v + "18" + conv, v + "19" + conv, v + "20" + conv, v + "21" + conv, v + "22: names RehydrateOnly in a string", v + "23" + conv}
	if !slices.Equal(got, want) {
		t.Fatalf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if strings.Count(body, ".RehydrateOnly(") != 0 {
		t.Fatal("stub check: the fixture must hold no textual call for substring matching to find")
	}
}

// TestArchInternalGetCallers asserts rule 2 (test files skipped: this gate
// names the symbol).
func TestArchInternalGetCallers(t *testing.T) {
	root := archModuleRoot(t)
	for _, dir := range []string{"internal", "cmd", "pkg"} {
		walkErr := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			data, rerr := os.ReadFile(path) //nolint:gosec // path comes from the module tree
			if rerr == nil && strings.Contains(string(data), "internalGet(") && !strings.HasPrefix(path, filepath.Join(root, "internal", "secrets")+string(filepath.Separator)) {
				t.Fatalf("%s references internalGet; the unelevated vault read never leaves internal/secrets", path)
			}
			return rerr
		})
		if walkErr != nil {
			t.Fatalf("walking %s: %v", dir, walkErr)
		}
	}
}
