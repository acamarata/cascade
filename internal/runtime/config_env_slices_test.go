package runtime

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type envSliceSite struct {
	file string
	name string
}

// dynamicEnvReadSlices binds dynamic-read table rows to their literal slices.
var dynamicEnvReadSlices = map[string]envSliceSite{
	"internal/ci/waitmerge_deps.go:82": {"internal/ci/waitmerge_deps.go", "waitTokenEnvNames"},
	"plugins/github/token_env.go:41":   {"plugins/github/token_env.go", "githubTokenEnvNames"},
}

// envSliceDrift compares both sets, failing closed for unreadable declarations.
func envSliceDrift(t *testing.T, root string, table map[string][]string, sites map[string]envSliceSite) []string {
	t.Helper()
	var drift []string
	for row, site := range sites {
		prefix := fmt.Sprintf("%s %s", site.file, site.name)
		names, err := envSliceNames(filepath.Join(root, filepath.FromSlash(site.file)), site.name)
		if err != nil {
			drift = append(drift, prefix+": "+err.Error())
			continue
		}
		declared, ok := table[row]
		if !ok {
			drift = append(drift, prefix+": missing dynamicEnvReads row "+row)
			continue
		}
		actual, expected := map[string]bool{}, map[string]bool{}
		for _, name := range names {
			actual[name] = true
		}
		for _, name := range declared {
			expected[name] = true
		}
		for name := range actual {
			if !expected[name] {
				drift = append(drift, prefix+": "+name+" missing from table")
			}
		}
		for name := range expected {
			if !actual[name] {
				drift = append(drift, prefix+": "+name+" missing from slice")
			}
		}
	}
	sort.Strings(drift)
	return drift
}

func envSliceNames(path, name string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return nil, err
	}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			for i, ident := range value.Names {
				if ident.Name != name {
					continue
				}
				if i >= len(value.Values) {
					return nil, fmt.Errorf("missing []string literal")
				}
				return envSliceLiterals(value.Values[i])
			}
		}
	}
	return nil, fmt.Errorf("missing variable")
}

func envSliceLiterals(expr ast.Expr) ([]string, error) {
	literal, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, fmt.Errorf("expected []string composite literal")
	}
	array, ok := literal.Type.(*ast.ArrayType)
	if !ok || array.Len != nil {
		return nil, fmt.Errorf("expected []string composite literal")
	}
	elem, ok := array.Elt.(*ast.Ident)
	if !ok || elem.Name != "string" {
		return nil, fmt.Errorf("expected []string composite literal")
	}
	var names []string
	for _, expr := range literal.Elts {
		value, ok := expr.(*ast.BasicLit)
		if !ok || value.Kind != token.STRING {
			return nil, fmt.Errorf("non-string literal element %s", astElementName(expr))
		}
		name, err := strconv.Unquote(value.Value)
		if err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}

func astElementName(expr ast.Expr) string {
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return fmt.Sprintf("%T", expr)
}

// TestDynamicEnvReadSlicesMatchTable guards slice additions and stale table names.
func TestDynamicEnvReadSlicesMatchTable(t *testing.T) {
	if len(dynamicEnvReadSlices) != 2 {
		t.Fatalf("slice sites = %d, want 2", len(dynamicEnvReadSlices))
	}
	for _, row := range []string{"internal/ci/waitmerge_deps.go:82", "plugins/github/token_env.go:41"} {
		if _, ok := dynamicEnvReadSlices[row]; !ok {
			t.Errorf("missing slice site %s", row)
		}
		if _, ok := dynamicEnvReads[row]; !ok {
			t.Errorf("missing dynamicEnvReads row %s", row)
		}
	}
	if drift := envSliceDrift(t, filepath.Join("..", ".."), dynamicEnvReads, dynamicEnvReadSlices); len(drift) != 0 {
		t.Fatalf("slice declaration drift: %v", drift)
	}
	testSeededEnvSliceDrift(t)
}

func testSeededEnvSliceDrift(t *testing.T) {
	cases := []struct {
		name, source, want string
		row                []string
	}{
		{"extra slice name", `var names = []string{"KNOWN", "EXTRA"}`, "EXTRA", []string{"KNOWN"}},
		{"extra table name", `var names = []string{"KNOWN"}`, "STALE", []string{"KNOWN", "STALE"}},
		{"identifier element", `const EXTRA = "EXTRA"; var names = []string{EXTRA}`, "EXTRA", []string{"EXTRA"}},
		{"missing variable", `var other = []string{"KNOWN"}`, "missing variable", []string{"KNOWN"}},
		{"missing value", `var names []string`, "missing []string literal", nil},
		{"nonliteral value", `var names = make([]string, 0)`, "composite literal", nil},
		{"array literal", `var names = [1]string{"KNOWN"}`, "composite literal", nil},
		{"wrong element type", `var names = []int{1}`, "composite literal", nil},
		{"missing row", `var names = []string{"KNOWN"}`, "missing dynamicEnvReads row", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module seeded\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "slice.go"), []byte("package seeded\n"+c.source+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			table := map[string][]string{"slice.go:1": c.row}
			if c.name == "missing row" {
				delete(table, "slice.go:1")
			}
			drift := envSliceDrift(t, root, table, map[string]envSliceSite{"slice.go:1": {"slice.go", "names"}})
			if len(drift) != 1 {
				t.Fatalf("drift = %v, want exactly one mismatch", drift)
			}
			for _, part := range []string{"slice.go", "names", c.want} {
				if !strings.Contains(drift[0], part) {
					t.Errorf("mismatch %q does not name %q", drift[0], part)
				}
			}
		})
	}
}
