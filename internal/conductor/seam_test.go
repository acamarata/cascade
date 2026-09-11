// Purpose: the depguard/arch fixture (task 8): no non-test .go file outside
//
//	internal/conductor/ may call a pkg/provider.ModelProvider method
//	directly. Per R-40.X10 this file carries NO allowlist entry for the
//	embeddings adapter package under providers/ - the allowlist is empty
//	by design. J/S-19.T6's ProviderEmbedder used to hold a
//	provider.ModelProvider field and call its Embed method directly; the
//	R-40.X10 defect fix retired that by routing the call through
//	embed.go's Executor.Embed (Pipeline.embedCapability, the same
//	sole-dispatch pattern execute.go's Chat path uses) instead of the
//	sensitivity Intercept alone - the Intercept never touches a
//	ModelProvider, so it could not have silenced this gate on its own.
package conductor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const providerImportPath = "github.com/acamarata/cascade/pkg/provider"

var seamVerbs = map[string]bool{
	"Chat": true, "Embed": true, "Count": true, "Stream": true, "Capabilities": true,
}

func seamModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("seam gate: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("seam gate: no go.mod found walking up")
		}
		dir = parent
	}
}

// seamProviderAlias returns the local import name pkg/provider is bound to
// in file, or "" if the file does not import it.
func seamProviderAlias(file *ast.File) string {
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if path != providerImportPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "provider"
	}
	return ""
}

// seamProviderVars collects identifier names declared (as a var, struct
// field, or function parameter) with the exact type "<alias>.ModelProvider"
// in file.
func seamProviderVars(file *ast.File, alias string) map[string]bool {
	vars := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		field, ok := n.(*ast.Field)
		if !ok || !seamIsModelProviderType(field.Type, alias) {
			return true
		}
		for _, name := range field.Names {
			vars[name.Name] = true
		}
		return true
	})
	return vars
}

func seamIsModelProviderType(expr ast.Expr, alias string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "ModelProvider" {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == alias
}

// seamRightmostIdent returns the final identifier in a selector chain
// (e.g. "mp" for "p.mp", "mp" for a bare "mp").
func seamRightmostIdent(expr ast.Expr) (string, bool) {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name, true
	case *ast.SelectorExpr:
		return e.Sel.Name, true
	}
	return "", false
}

// seamFindViolations reports one string per direct ModelProvider verb call
// found in file on a receiver name in vars.
func seamFindViolations(file *ast.File, vars map[string]bool, fset *token.FileSet, relPath string) []string {
	var found []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !seamVerbs[sel.Sel.Name] {
			return true
		}
		name, ok := seamRightmostIdent(sel.X)
		if !ok || !vars[name] {
			return true
		}
		pos := fset.Position(call.Pos())
		found = append(found, relPath+":"+sel.Sel.Name+" (line "+itoa(pos.Line)+")")
		return true
	})
	return found
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TestSeam_NoDirectModelProviderCallOutsideConductor is the depguard/arch
// fixture task 8 describes.
func TestSeam_NoDirectModelProviderCallOutsideConductor(t *testing.T) {
	root := seamModuleRoot(t)
	fset := token.NewFileSet()
	var violations []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if strings.Contains(rel, string(filepath.Separator)+".") || strings.HasPrefix(rel, "internal"+string(filepath.Separator)+"conductor"+string(filepath.Separator)) {
			return nil
		}
		if strings.Contains(rel, "testdata") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		alias := seamProviderAlias(file)
		if alias == "" {
			return nil
		}
		vars := seamProviderVars(file, alias)
		violations = append(violations, seamFindViolations(file, vars, fset, rel)...)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("seam gate: walking module tree: %v", walkErr)
	}
	if len(violations) == 0 {
		return
	}
	t.Logf("KNOWN, out-of-scope violations this gate is designed to surface (R-40.X10, retired by S-22.T3):")
	for _, v := range violations {
		t.Logf("  %s", v)
	}
	t.Errorf("%d direct pkg/provider.ModelProvider call(s) outside internal/conductor/ - the door is not sole; see log above", len(violations))
}
