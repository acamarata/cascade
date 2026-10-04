package runtime

// Purpose: gates reservedEnvVars against the CASCADE_* names the tree
//   reads: a new dedicated variable that is not reserved would be
//   mistaken for a generic override candidate and could be warned about or
//   applied as one.
// Inputs: every non-test .go file under the repo root, twice: parsed with
//   go/parser (string literals, every file whatever its build tags) and
//   loaded with go/types through golang.org/x/tools/go/packages (any
//   constant string expression, so `const envX = "CASCADE_X";
//   os.Getenv(envX)` counts, for the host build configuration).
// Outputs: n/a (test-only).
// Constraints: a CASCADE_* string counts when it is the first argument of a
//   call to os.Getenv/os.LookupEnv or to a Getenv-style seam (any function
//   or method whose name contains getenv or lookupenv, case-insensitive)
//   and is not of the SECTION__KEY form. A call whose first argument is not
//   a compile-time constant is a dynamic read: it must be declared in
//   dynamicEnvReads (config_env_warn_test.go, file:line -> env names), and
//   a CASCADE_* name listed there must be reserved. The typed pass sees the
//   host build configuration only.
// SPORT: runtime/config (ADD, P1-CORE-16).

import (
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// envLiteralsRead returns every CASCADE_* literal read through an env
// accessor call in the non-test Go files of the repo at root, mapped to one
// file:line where it appears.
func envLiteralsRead(t *testing.T, root string) map[string]string {
	t.Helper()
	found := map[string]string{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() && path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor" || name == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if lit, ok := envReadLiteral(n); ok {
				found[lit] = fset.Position(n.Pos()).String()
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return found
}

// envAccessorArg returns the first argument of a call to an env accessor
// (a function or method whose name contains getenv or lookupenv), or false
// when n is not one.
func envAccessorArg(n ast.Node) (ast.Expr, bool) {
	call, ok := n.(*ast.CallExpr)
	if !ok || len(call.Args) == 0 {
		return nil, false
	}
	var fn string
	switch f := call.Fun.(type) {
	case *ast.SelectorExpr:
		fn = f.Sel.Name
	case *ast.Ident:
		fn = f.Name
	}
	lower := strings.ToLower(fn)
	if !strings.Contains(lower, "getenv") && !strings.Contains(lower, "lookupenv") {
		return nil, false
	}
	return call.Args[0], true
}

// envReadLiteral extracts the CASCADE_* first-argument literal of an
// env-accessor call, or false when n is not one.
func envReadLiteral(n ast.Node) (string, bool) {
	arg, ok := envAccessorArg(n)
	if !ok {
		return "", false
	}
	lit, ok := arg.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil || !strings.HasPrefix(s, "CASCADE_") {
		return "", false
	}
	return s, true
}

// envReadConst resolves the first argument of an env-accessor call through
// go/types: any constant string expression (a literal, a named const, a
// const from another package, a constant concatenation) is returned when
// it is a CASCADE_* name.
func envReadConst(info *types.Info, n ast.Node) (string, bool) {
	arg, ok := envAccessorArg(n)
	if !ok {
		return "", false
	}
	tv, ok := info.Types[arg]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
		return "", false
	}
	s := constant.StringVal(tv.Value)
	return s, strings.HasPrefix(s, "CASCADE_")
}

// envReadDynamic reports whether n is an env-accessor call whose first
// argument is not a compile-time constant.
func envReadDynamic(info *types.Info, n ast.Node) bool {
	arg, ok := envAccessorArg(n)
	if !ok {
		return false
	}
	tv, ok := info.Types[arg]
	return !ok || tv.Value == nil
}

// envConstsRead loads the non-test packages matching patterns under dir with
// type information and returns every CASCADE_* constant read through an env
// accessor call, mapped to one file:line where it appears, and the set of
// dynamic reads as slash-separated file:line relative to dir.
func envConstsRead(t *testing.T, dir string, env []string, patterns ...string) (map[string]string, map[string]bool) {
	t.Helper()
	base, err := filepath.Abs(dir)
	if err == nil {
		base, err = filepath.EvalSymlinks(base)
	}
	if err != nil {
		t.Fatal(err)
	}
	cfg := &packages.Config{Dir: dir, Env: env, Mode: packages.NeedName | packages.NeedFiles |
		packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		t.Fatalf("load %v in %s: %v", patterns, dir, err)
	}
	found, dynamic := map[string]string{}, map[string]bool{}
	for _, p := range pkgs {
		for _, e := range p.Errors {
			t.Fatalf("load %s: %v", p.PkgPath, e)
		}
		for _, f := range p.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				if s, ok := envReadConst(p.TypesInfo, n); ok {
					found[s] = p.Fset.Position(n.Pos()).String()
				}
				if envReadDynamic(p.TypesInfo, n) {
					pos := p.Fset.Position(n.Pos())
					file, err := filepath.EvalSymlinks(pos.Filename)
					if err == nil {
						file, err = filepath.Rel(base, file)
					}
					if err != nil {
						t.Fatalf("locate %s: %v", pos, err)
					}
					dynamic[filepath.ToSlash(file)+":"+strconv.Itoa(pos.Line)] = true
				}
				return true
			})
		}
	}
	return found, dynamic
}

// uncoveredEnvNames returns the names that are neither section__key
// overrides nor reserved, sorted.
func uncoveredEnvNames(found map[string]string) []string {
	var missing []string
	for name := range found {
		if envOverrideKey(name) != "" || isReservedEnvName(name) {
			continue
		}
		missing = append(missing, name)
	}
	sort.Strings(missing)
	return missing
}

func TestReservedEnvVarsCoverTree(t *testing.T) {
	root := filepath.Join("..", "..")
	found := envLiteralsRead(t, root)
	if len(found) < 3 {
		t.Fatalf("scanner found only %d CASCADE_* reads (%v); it is not seeing the tree", len(found), found)
	}
	if _, ok := found["CASCADE_HOME"]; !ok {
		t.Fatalf("scanner did not see CASCADE_HOME in paths.go; it is not seeing the tree")
	}
	consts, _ := envConstsRead(t, root, os.Environ(), "./...")
	if _, ok := consts["CASCADE_HOME"]; !ok || len(consts) < len(found)/2 {
		t.Fatalf("typed scanner found %d names without CASCADE_HOME; it is not seeing the tree", len(consts))
	}
	for name, at := range consts {
		if _, ok := found[name]; !ok {
			found[name] = at
		}
	}
	for _, m := range uncoveredEnvNames(found) {
		t.Errorf("%s (read at %s) is not in reservedEnvVars", m, found[m])
	}
}

// TestReservedEnvVarsGateCatchesNewLiteral proves the gate can fail: a
// seeded new dedicated variable is reported as uncovered.
func TestReservedEnvVarsGateCatchesNewLiteral(t *testing.T) {
	seeded := map[string]string{"CASCADE_HOME": "x:1", "CASCADE_BRAND_NEW_SWITCH": "y:2", "CASCADE_INIT_NAME": "z:3", "CASCADE_A__B": "w:4"}
	got := uncoveredEnvNames(seeded)
	if len(got) != 1 || got[0] != "CASCADE_BRAND_NEW_SWITCH" {
		t.Fatalf("uncovered = %v, want exactly [CASCADE_BRAND_NEW_SWITCH]", got)
	}
}

// TestReservedEnvVarsCatchConstRoutedRead proves the typed scanner sees a
// name routed through a string const, which a literal-only scan misses: a
// seeded package reading os.Getenv(envX) is reported as uncovered.
func TestReservedEnvVarsCatchConstRoutedRead(t *testing.T) {
	dir, env := seedEnvModule(t, "package main\n\nimport \"os\"\n\nconst envX = \"CASCADE_SEEDED_CONST\"\n\nfunc main() { _ = os.Getenv(envX) }\n")
	found, _ := envConstsRead(t, dir, env, "./...")
	if got := uncoveredEnvNames(found); len(got) != 1 || got[0] != "CASCADE_SEEDED_CONST" {
		t.Fatalf("uncovered = %v (found %v), want exactly [CASCADE_SEEDED_CONST]", got, found)
	}
	if lits := envLiteralsRead(t, dir); len(lits) != 0 {
		t.Fatalf("literal scan saw %v; the seed must be const-routed only", lits)
	}
}

// seedEnvModule writes a one-file module (main.go = src) and returns its
// directory and the go env that loads it offline.
func seedEnvModule(t *testing.T, src string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"go.mod": "module seeded\n\ngo 1.21\n", "main.go": src} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local")
}

// dynamicReadDrift returns the dynamic reads missing from table and the
// table entries no longer found, each sorted.
func dynamicReadDrift(found map[string]bool, table map[string][]string) (undeclared, stale []string) {
	for at := range found {
		if _, ok := table[at]; !ok {
			undeclared = append(undeclared, at)
		}
	}
	for at := range table {
		if !found[at] {
			stale = append(stale, at)
		}
	}
	sort.Strings(undeclared)
	sort.Strings(stale)
	return undeclared, stale
}
