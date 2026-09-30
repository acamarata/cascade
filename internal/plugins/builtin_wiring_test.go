package plugins

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: prove internal/plugins/registry.go holds no plugin-specific
//   generator wiring or imports (composition-root-registration, DEBT-ARCH-11).
//   Wiring belongs in per-plugin files (builtin_codex_wiring.go,
//   builtin_opencode_wiring.go).
// SPORT: internal/plugins wiring tests (ADD) — P1-CORE-02.

// checkRegistryAST inspects an AST-parsed file representing registry.go
// and returns all violations: any init() function, or any import of
// plugins/codex or plugins/opencode.
func checkRegistryAST(file *ast.File) []string {
	var violations []string

	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if strings.HasSuffix(path, "plugins/codex") || strings.Contains(path, "/plugins/codex") {
			violations = append(violations, "registry.go imports plugins/codex: "+path)
		}
		if strings.HasSuffix(path, "plugins/opencode") || strings.Contains(path, "/plugins/opencode") {
			violations = append(violations, "registry.go imports plugins/opencode: "+path)
		}
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == "init" {
			violations = append(violations, "registry.go contains an init() function")
		}
	}

	return violations
}

func assertSeededMutation(t *testing.T, fset *token.FileSet, name, src string) {
	t.Helper()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatalf("parsing seeded fixture %s: %v", name, err)
	}
	if len(checkRegistryAST(f)) == 0 {
		t.Fatalf("seeded mutation %s did not trigger violation", name)
	}
}

// TestRegistryHoldsNoPluginWiring proves internal/plugins/registry.go has no
// init() and imports neither plugins/codex nor plugins/opencode.
func TestRegistryHoldsNoPluginWiring(t *testing.T) {
	path := "registry.go"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		path = filepath.Join("internal", "plugins", "registry.go")
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	for _, v := range checkRegistryAST(file) {
		t.Errorf("composition-root-registration violation: %s", v)
	}

	// Seeded mutations: prove that re-adding imports or an init() function
	// fails the check (C8, DEBT-ARCH-11).
	assertSeededMutation(t, fset, "seeded_codex.go", "package plugins\nimport _ \"github.com/acamarata/cascade/plugins/codex\"\n")
	assertSeededMutation(t, fset, "seeded_opencode.go", "package plugins\nimport _ \"github.com/acamarata/cascade/plugins/opencode\"\n")
	assertSeededMutation(t, fset, "seeded_init.go", "package plugins\nfunc init() {}\n")
}
