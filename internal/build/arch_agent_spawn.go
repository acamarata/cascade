package build

// Purpose: the agent-spawn arch gates (P1-AGT-02): only internal/providers/agents
// imports a providers/agents/<driver>; only its spawn.go and the conformance
// package call an AgentProvider Spawn; conformance has no non-test importer;
// no vendor executable is exec'd by name outside the agents packages.
// Constraints: non-test files; typed passes load each flagged package (more
// builds, every GOOS and the integration tag, for files the host build skips)
// and fail on a file no build compiled. Spawn is found by resolved method and
// signature, not text; reflection and exec names held in variables are out of reach.

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// AgentSpawnViolation is one gate finding.
type AgentSpawnViolation struct {
	File, What string
	Line       int
}

const (
	agentModule        = "github.com/acamarata/cascade"
	agentDriverPrefix  = agentModule + "/providers/agents/"
	agentConformancePk = agentDriverPrefix + "conformance"
	agentShimDir       = "internal/providers/agents/"
)

// agentFiles parses every non-test .go file under root, tags ignored.
func agentFiles(root string, mode parser.Mode) (map[string]*ast.File, *token.FileSet, error) {
	fset, out := token.NewFileSet(), map[string]*ast.File{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && p != root && egressSkippedDir(d.Name()):
			return filepath.SkipDir
		case d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go"):
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, mode|parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out[filepath.ToSlash(rel)] = f
		return nil
	})
	return out, fset, err
}

// agentImportScan reports each import whose path satisfies bad(rel, path).
func agentImportScan(root string, bad func(rel, imp string) string) ([]AgentSpawnViolation, error) {
	files, fset, err := agentFiles(root, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var out []AgentSpawnViolation
	for rel, f := range files {
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if what := bad(rel, strings.ToLower(p)); what != "" {
				out = append(out, AgentSpawnViolation{rel, what + " " + p, fset.Position(imp.Pos()).Line})
			}
		}
	}
	return agentSorted(out), nil
}

// ScanAgentDriverImporters reports a file importing a providers/agents/<driver>
// from outside internal/providers/agents/ and that driver's own tree.
func ScanAgentDriverImporters(root string) ([]AgentSpawnViolation, error) {
	return agentImportScan(root, func(rel, imp string) string {
		name, ok := strings.CutPrefix(imp, agentDriverPrefix)
		if !ok || imp == agentConformancePk || strings.HasPrefix(imp, agentConformancePk+"/") {
			return ""
		}
		own := "providers/agents/" + strings.SplitN(name, "/", 2)[0] + "/"
		if path.Dir(rel)+"/" == agentShimDir || strings.HasPrefix(rel, own) {
			return ""
		}
		return "imports the driver package"
	})
}

// ScanSpawnCallers reports an AgentProvider Spawn call or method value outside the shim and conformance.
func ScanSpawnCallers(root string) ([]AgentSpawnViolation, error) {
	return scanSpawn(root, func(rel string) bool {
		return rel == agentShimDir+"spawn.go" || strings.HasPrefix(rel, "providers/agents/conformance/")
	})
}

func scanSpawn(root string, allowed func(rel string) bool) ([]AgentSpawnViolation, error) {
	hint := func(n ast.Node) bool { sel, ok := n.(*ast.SelectorExpr); return ok && sel.Sel.Name == "Spawn" }
	return agentTypedScan(root, hint, func(pkg *packages.Package, f *ast.File, rel string, report func(ast.Node, string)) {
		if allowed(rel) {
			return
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Spawn" && agentIsSpawn(pkg.TypesInfo, sel) {
				report(sel, "Spawn used outside the shim")
			}
			return true
		})
	})
}

func agentIsSpawn(info *types.Info, sel *ast.SelectorExpr) bool {
	s := info.Selections[sel]
	if s == nil {
		return false
	}
	fn, ok := s.Obj().(*types.Func)
	if !ok || fn.Pkg() == nil {
		return false
	}
	if strings.HasPrefix(fn.Pkg().Path(), agentDriverPrefix) {
		return true
	}
	sig := fn.Type().(*types.Signature)
	if sig.Params().Len() != 2 {
		return false
	}
	if _, generic := sig.Params().At(1).Type().(*types.TypeParam); generic {
		return true // a Spawn reached through a type parameter cannot be told from the driver's
	}
	named, ok := types.Unalias(sig.Params().At(1).Type()).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == agentModule+"/pkg/provider" && named.Obj().Name() == "AgentJobSpec"
}

// ScanHarnessExec reports os/exec Command(Context) of a constant program name
// whose basename (any case) is one of binaries, or a use of either function as
// a value, outside providers/agents and internal/providers/agents (m-11).
func ScanHarnessExec(root string, binaries []string) ([]AgentSpawnViolation, error) {
	hint := func(n ast.Node) bool { is, ok := n.(*ast.ImportSpec); return ok && is.Path.Value == `"os/exec"` }
	return agentTypedScan(root, hint, func(pkg *packages.Package, f *ast.File, rel string, report func(ast.Node, string)) {
		if strings.HasPrefix(rel, "providers/agents/") || strings.HasPrefix(rel, agentShimDir) {
			return
		}
		called := map[*ast.Ident]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				id := funIdent(x.Fun)
				called[id] = true
				i, ok := execFunc(pkg.TypesInfo, id)
				if !ok || i >= len(x.Args) {
					return true
				}
				if tv := pkg.TypesInfo.Types[x.Args[i]]; tv.Value != nil && tv.Value.Kind() == constant.String {
					base := strings.ToLower(path.Base(strings.ReplaceAll(constant.StringVal(tv.Value), `\`, "/")))
					for _, ext := range []string{".exe", ".cmd", ".bat", ".com", ".ps1"} {
						base = strings.TrimSuffix(base, ext)
					}
					if slices.ContainsFunc(binaries, func(b string) bool { return strings.EqualFold(b, base) }) {
						report(x, "exec of the vendor harness "+base)
					}
				}
			case *ast.Ident:
				if _, ok := execFunc(pkg.TypesInfo, x); ok && !called[x] {
					report(x, "os/exec Command used as a value: the program name cannot be checked")
				}
			}
			return true
		})
	})
}

// funIdent returns the identifier a call's function expression names.
func funIdent(e ast.Expr) *ast.Ident {
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident:
		return x
	case *ast.SelectorExpr:
		return x.Sel
	}
	return nil
}

// execFunc reports whether id uses os/exec Command or CommandContext, and
// the index of its program-name argument.
func execFunc(info *types.Info, id *ast.Ident) (int, bool) {
	fn, _ := info.Uses[id].(*types.Func)
	if fn == nil || fn.Pkg() == nil || fn.Pkg().Path() != "os/exec" {
		return 0, false
	}
	i, ok := map[string]int{"Command": 0, "CommandContext": 1}[fn.Name()]
	return i, ok
}

// agentTypedScan type-checks every package holding a node hint accepts, once
// per GOOS and tag, and runs check on each flagged file.
func agentTypedScan(root string, hint func(ast.Node) bool, check agentCheck) ([]AgentSpawnViolation, error) {
	files, _, err := agentFiles(root, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	flagged := map[string]bool{}
	for rel, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			flagged[rel] = flagged[rel] || hint(n)
			return true
		})
	}
	st := agentState{root: root, flagged: flagged, checked: map[string]bool{}, found: map[AgentSpawnViolation]bool{}, check: check}
	// Host build first; a flagged file it left out sends its package through the others.
	cfgs := [][2]string{{runtime.GOOS, ""}}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		cfgs = append(cfgs, [2]string{goos, ""}, [2]string{goos, "integration"})
	}
	for _, c := range cfgs {
		pending := map[string]bool{}
		for rel := range flagged {
			if flagged[rel] && !st.checked[rel] {
				pending["./"+path.Dir(rel)] = true
			}
		}
		if len(pending) == 0 {
			break
		}
		if err := st.load(c[0], c[1], slices.Sorted(maps.Keys(pending))); err != nil {
			return nil, err
		}
	}
	for rel, on := range flagged {
		if on && !st.checked[rel] {
			return nil, fmt.Errorf("agent gate: no build configuration type-checks %s", rel)
		}
	}
	return agentSorted(slices.Collect(maps.Keys(st.found))), nil
}

type agentCheck func(pkg *packages.Package, f *ast.File, rel string, report func(ast.Node, string))

type agentState struct {
	root             string
	flagged, checked map[string]bool
	found            map[AgentSpawnViolation]bool
	check            agentCheck
}

func (st *agentState) load(goos, tag string, dirs []string) error {
	cfg := &packages.Config{Dir: st.root, Env: append(os.Environ(), "GOOS="+goos, "GOARCH=amd64", "CGO_ENABLED=0", "GOWORK=off"),
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo}
	if tag != "" {
		cfg.BuildFlags = []string{"-tags=" + tag}
	}
	pkgs, err := packages.Load(cfg, dirs...)
	if err != nil {
		return err
	}
	for _, pkg := range pkgs {
		if len(pkg.GoFiles) == 0 && (len(pkg.Errors) == 0 || strings.Contains(pkg.Errors[0].Msg, "build constraints exclude")) {
			continue
		}
		if len(pkg.Errors) != 0 || len(pkg.Syntax) != len(pkg.CompiledGoFiles) {
			return fmt.Errorf("agent gate: %s/%s %s: %v (syntax %d of %d files)", goos, tag, pkg.PkgPath, pkg.Errors, len(pkg.Syntax), len(pkg.CompiledGoFiles))
		}
		for _, f := range pkg.Syntax {
			rel, _ := filepath.Rel(st.root, pkg.Fset.Position(f.Pos()).Filename)
			rel = filepath.ToSlash(rel)
			st.checked[rel] = true // every file of a loaded package is checked, not only the flagged ones
			st.check(pkg, f, rel, func(n ast.Node, what string) {
				st.found[AgentSpawnViolation{rel, what, pkg.Fset.Position(n.Pos()).Line}] = true
			})
		}
	}
	return nil
}

func agentSorted(v []AgentSpawnViolation) []AgentSpawnViolation {
	slices.SortFunc(v, func(a, b AgentSpawnViolation) int { return cmp.Or(strings.Compare(a.File, b.File), a.Line-b.Line) })
	return v
}
