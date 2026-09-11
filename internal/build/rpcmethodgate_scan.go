// Package build (this file) holds rpcmethodgate.go's AST walking: finding
// `.Do(...)` and `.Register(...)` call sites, and resolving a method-name
// argument to a compile-time string. Split out under Art.10.3's 300-line
// cap, matching ledgeridentitygate.go/_coverage.go's own split.
package build

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// constLiteralMap resolves an unqualified const name to its string value,
// scoped to one package directory (see resolvePackageConsts).
type constLiteralMap map[string]string

// doCall is one `.Do(...)` call site this scan found, carrying the
// unresolved method-name expression for resolveMethodArg to inspect.
type doCall struct {
	line      int
	methodArg ast.Expr
}

// isCLISourceFile restricts the `.Do` call-site scan to cmd/cascade's own
// non-test files - R-16.80 Ruling 3(c)'s own wording ("every method name
// a CLI c.Do call passes"). A tree-wide `.Do` scan would ALSO flag
// pkg/provider.Client.ModelExecute's "model.execute" call
// (pkg/provider/client.go:65) as unregistered - a real, separate,
// pre-existing gap (grep confirms zero production Register("model.
// execute", ...) callers anywhere), but fixing it is a different door
// than R-16.80's conductor.execute connector and out of this ticket's
// authorized scope (no new ticket may be created - 18-T0-RULINGS-R16.md's
// own Ruling 2). Scoping this gate to cmd/cascade avoids shipping it
// already red for a defect this ticket does not fix; see this file's
// package doc comment and this ticket's journal for the disclosure.
func isCLISourceFile(rel string) bool {
	p := filepath.ToSlash(rel)
	return strings.Contains(p, "cmd/cascade/") && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go")
}

// sortedCopy returns a sorted copy of files so scan order is deterministic.
func sortedCopy(files []string) []string {
	out := append([]string(nil), files...)
	sort.Strings(out)
	return out
}

// parseTracked parses one module-relative file, returning (nil, nil, nil)
// if it no longer exists on disk (a tracked-but-deleted path, matching
// ledgeridentitygate_coverage.go's own os.IsNotExist handling).
func parseTracked(root, rel string) (*token.FileSet, *ast.File, error) {
	full := filepath.Join(root, rel)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, full, nil, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	return fset, f, nil
}

// findDoCalls returns every `<x>.Do(ctx, <method>, params, out)` call
// site in rel: a call expression whose selector method is named "Do"
// with EXACTLY four arguments, the shape every JSON-RPC caller in this
// tree declares (internal/client.Client.Do, pkg/provider.RPCCaller.Do,
// and every generated fleet/policy client wrapper: `Do(ctx, method,
// params, out) error`). Exactly four excludes the unrelated two-argument
// `Do(ctx, req)` HTTP-Doer shape (providers/*/stream.go,
// internal/providers/intake/transport.go, internal/ci/poll.go) - a
// different interface entirely that happens to share the method name
// "Do" but never names an RPC method.
func findDoCalls(root, rel string) ([]doCall, error) {
	fset, f, err := parseTracked(root, rel)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, nil
	}
	var out []doCall
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Do" || len(call.Args) != 4 {
			return true
		}
		out = append(out, doCall{line: fset.Position(call.Pos()).Line, methodArg: call.Args[1]})
		return true
	})
	return out, nil
}

// collectRegisteredMethods scans every non-test .go file in files for
// `<x>.Register(<method>, ...)` call sites (an rpc.Registry.Register or
// alike single-string-then-handler shape) and returns the set of every
// resolvable method name found. An unresolvable Register argument is
// simply skipped here - a registration this gate cannot name can neither
// confirm nor deny coverage, so it is treated as absent rather than
// fabricating a match (the safe direction: it can only ever make this
// gate MORE likely to flag a real gap, never mask one).
func collectRegisteredMethods(root string, files []string) (map[string]bool, error) {
	registered := map[string]bool{}
	cache := map[string]constLiteralMap{}
	for _, rel := range sortedCopy(files) {
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			continue
		}
		_, f, err := parseTracked(root, rel)
		if err != nil {
			return nil, err
		}
		if f == nil {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Register" || len(call.Args) < 1 {
				return true
			}
			if method, ok := resolveMethodArg(root, rel, call.Args[0], cache); ok {
				registered[method] = true
			}
			return true
		})
	}
	return registered, nil
}

// resolveMethodArg resolves expr - the method-name argument at some call
// site in file rel - to a compile-time string: a bare string literal, an
// identifier bound to a const in rel's OWN file, or a qualified
// <pkg>.Const whose import this scan can follow into that package's own
// directory (resolvePackageConsts). Any other shape (a parameter, a
// function call, a struct field) is unresolved.
func resolveMethodArg(root, rel string, expr ast.Expr, cache map[string]constLiteralMap) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(e.Value)
		return v, err == nil
	case *ast.Ident:
		consts, err := resolvePackageConsts(root, filepath.Dir(rel), cache)
		if err != nil {
			return "", false
		}
		v, ok := consts[e.Name]
		return v, ok
	case *ast.SelectorExpr:
		pkgIdent, ok := e.X.(*ast.Ident)
		if !ok {
			return "", false
		}
		dir, ok := resolveImportDir(root, rel, pkgIdent.Name)
		if !ok {
			return "", false
		}
		consts, err := resolvePackageConsts(root, dir, cache)
		if err != nil {
			return "", false
		}
		v, ok := consts[e.Sel.Name]
		return v, ok
	default:
		return "", false
	}
}

// resolveImportDir resolves alias (as used in file rel) to the module-
// relative directory its import path names, reading rel's own import
// declarations - never assumed from the alias text.
func resolveImportDir(root, rel, alias string) (string, bool) {
	_, f, err := parseTracked(root, rel)
	if err != nil || f == nil {
		return "", false
	}
	const modulePrefix = "github.com/acamarata/cascade/"
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(path)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name != alias || !strings.HasPrefix(path, modulePrefix) {
			continue
		}
		return strings.TrimPrefix(path, modulePrefix), true
	}
	return "", false
}

// resolvePackageConsts returns every top-level `const Name = "literal"`
// declaration across dir's own non-test .go files, caching per directory
// so a tree-wide scan parses each package's files at most once.
func resolvePackageConsts(root, dir string, cache map[string]constLiteralMap) (constLiteralMap, error) {
	if c, ok := cache[dir]; ok {
		return c, nil
	}
	out := constLiteralMap{}
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		cache[dir] = out
		return out, nil
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		_, f, err := parseTracked(root, filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if f == nil {
			continue
		}
		collectFileConsts(f, out)
	}
	cache[dir] = out
	return out, nil
}

// collectFileConsts adds every `const Name = "literal"` declaration in f
// to out (single or grouped const blocks alike).
func collectFileConsts(f *ast.File, out constLiteralMap) {
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != len(vs.Values) {
				continue
			}
			for i, name := range vs.Names {
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if v, err := strconv.Unquote(lit.Value); err == nil {
						out[name.Name] = v
					}
				}
			}
		}
	}
}
