// Package build (this file): the go/ast helpers behind the one-firewall,
// one-detector gates in arch_detector_test.go: parse the shipping tree,
// resolve an import's local names, find selector references through any
// alias, and find egress.Vault method pairs per package directory with the
// context import and context.Context aliases resolved.
package build

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// archSource is one parsed shipping file and its repo-relative path.
type archSource struct {
	rel  string
	file *ast.File
}

// shippingSources parses every non-test .go file under the module's code
// roots. testdata, vendor and dot directories are skipped.
func shippingSources(t *testing.T, root string) []archSource {
	t.Helper()
	var out []archSource
	fset := token.NewFileSet()
	for _, top := range []string{"cmd", "internal", "pkg", "plugins", "providers", "apps"} {
		dir := filepath.Join(root, top)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := d.Name()
			if d.IsDir() {
				if name == "testdata" || name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if perr != nil {
				return perr
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return rerr
			}
			out = append(out, archSource{rel: filepath.ToSlash(rel), file: f})
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}
	if len(out) == 0 {
		t.Fatal("no shipping Go files found; the gate would pass vacuously")
	}
	return out
}

// importNames returns the local names f binds importPath to ("." for a dot
// import), or nil when f does not import it.
func importNames(f *ast.File, importPath string) []string {
	var names []string
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != importPath {
			continue
		}
		if imp.Name == nil {
			names = append(names, filepath.Base(importPath))
			continue
		}
		names = append(names, imp.Name.Name)
	}
	return names
}

// selectorRefs reports every reference to importPath.symbol in f, through
// any alias, as a call or as a value. A dot import of importPath is itself
// a reference: it would make the bare name reachable.
func selectorRefs(f *ast.File, importPath, symbol string) int {
	names := importNames(f, importPath)
	if len(names) == 0 {
		return 0
	}
	local := map[string]bool{}
	n := 0
	for _, name := range names {
		if name == "." {
			n++
			continue
		}
		local[name] = true
	}
	ast.Inspect(f, func(node ast.Node) bool {
		sel, ok := node.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != symbol {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && local[id.Name] {
			n++
		}
		return true
	})
	return n
}

// underDir reports whether rel lies in dir (slash-separated, whole segments).
func underDir(rel, dir string) bool {
	return strings.HasPrefix(rel, dir+"/")
}

// receiverName strips pointers and type parameters from a receiver type.
func receiverName(expr ast.Expr) string {
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		case *ast.Ident:
			return e.Name
		default:
			return ""
		}
	}
}

// typeString renders the few type shapes egress.Vault's signature uses.
func typeString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return typeString(e.X) + "." + e.Sel.Name
	case *ast.ArrayType:
		if e.Len == nil {
			return "[]" + typeString(e.Elt)
		}
	}
	return "?"
}

// sameTypes reports whether got is exactly want.
func sameTypes(got []string, want ...string) bool {
	return strings.Join(got, ",") == strings.Join(want, ",")
}

// seededSources parses fixture files as if they lived at the given paths.
func seededSources(t *testing.T, files map[string]string) []archSource {
	t.Helper()
	fset := token.NewFileSet()
	var out []archSource
	for rel, src := range files {
		f, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("fixture %s: %v", rel, err)
		}
		out = append(out, archSource{rel: rel, file: f})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out
}

// vaultImplementers returns, per package directory, the receiver types that
// declare both halves of egress.Vault's method set, as "dir: type T". The
// halves may sit in different files of one package; the context parameter
// may be spelled through any import name of "context" or a package-level
// alias of context.Context.
func vaultImplementers(srcs []archSource) []string {
	byDir := map[string][]*ast.File{}
	for _, s := range srcs {
		dir := path.Dir(s.rel)
		byDir[dir] = append(byDir[dir], s.file)
	}
	var out []string
	for dir, files := range byDir {
		aliases := contextAliases(files)
		list, get := map[string]bool{}, map[string]bool{}
		for _, f := range files {
			isCtx := contextMatcher(f, aliases)
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
					continue
				}
				recv := receiverName(fn.Recv.List[0].Type)
				params, results := fieldExprs(fn.Type.Params), fieldExprs(fn.Type.Results)
				res := typeStrings(results)
				switch {
				case fn.Name.Name == "List" && len(params) == 1 && isCtx(params[0]) && sameTypes(res, "[]string", "error"):
					list[recv] = true
				case fn.Name.Name == "Get" && len(params) == 2 && isCtx(params[0]) &&
					typeString(params[1]) == "string" && sameTypes(res, "[]byte", "error"):
					get[recv] = true
				}
			}
		}
		for recv := range list {
			if get[recv] {
				out = append(out, dir+": type "+recv+" implements egress.Vault")
			}
		}
	}
	sort.Strings(out)
	return out
}

// contextAliases returns the package-level type names files declare as an
// alias of context.Context (type ctx = context.Context).
func contextAliases(files []*ast.File) map[string]bool {
	aliases := map[string]bool{}
	for _, f := range files {
		isCtx := contextMatcher(f, nil)
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok && ts.Assign.IsValid() && isCtx(ts.Type) {
					aliases[ts.Name.Name] = true
				}
			}
		}
	}
	return aliases
}

// contextMatcher reports whether an expression in f names context.Context:
// through any import name of "context", a dot import, or an alias.
func contextMatcher(f *ast.File, aliases map[string]bool) func(ast.Expr) bool {
	local, dot := map[string]bool{}, false
	for _, name := range importNames(f, "context") {
		local[name] = true
		dot = dot || name == "."
	}
	return func(expr ast.Expr) bool {
		switch e := expr.(type) {
		case *ast.SelectorExpr:
			id, ok := e.X.(*ast.Ident)
			return ok && local[id.Name] && e.Sel.Name == "Context"
		case *ast.Ident:
			return aliases[e.Name] || (dot && e.Name == "Context")
		}
		return false
	}
}

// fieldExprs returns each parameter or result type, one entry per name.
func fieldExprs(fl *ast.FieldList) []ast.Expr {
	if fl == nil {
		return nil
	}
	var out []ast.Expr
	for _, field := range fl.List {
		n := len(field.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			out = append(out, field.Type)
		}
	}
	return out
}

// typeStrings renders each expression with typeString.
func typeStrings(exprs []ast.Expr) []string {
	out := make([]string, 0, len(exprs))
	for _, e := range exprs {
		out = append(out, typeString(e))
	}
	return out
}
