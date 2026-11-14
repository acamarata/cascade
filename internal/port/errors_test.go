package port

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// taxonomy is the closed P1 error set: sentinel name, code and retryability.
var taxonomy = []struct {
	name      string
	err       PortError
	code      string
	retryable bool
}{
	{"ErrInvalid", ErrInvalid, "invalid", false},
	{"ErrNotFound", ErrNotFound, "not_found", false},
	{"ErrConflict", ErrConflict, "conflict", false},
	{"ErrDenied", ErrDenied, "denied", false},
	{"ErrUnavailable", ErrUnavailable, "unavailable", true},
	{"ErrTimeout", ErrTimeout, "timeout", true},
	{"ErrExhausted", ErrExhausted, "exhausted", true},
	{"ErrInternal", ErrInternal, "internal", false},
}

// TestErrorSetIsClosed asserts the package declares exactly the eight
// taxonomy sentinels, each bound to its code and retryability, and no
// other error values or error constructors.
func TestErrorSetIsClosed(t *testing.T) {
	seen := map[string]bool{}
	for _, tc := range taxonomy {
		if tc.err == nil {
			t.Fatalf("%s is nil", tc.name)
		}
		if got := tc.err.Code(); got != tc.code {
			t.Errorf("%s.Code() = %q, want %q", tc.name, got, tc.code)
		}
		if got := tc.err.Retryable(); got != tc.retryable {
			t.Errorf("%s.Retryable() = %v, want %v", tc.name, got, tc.retryable)
		}
		if seen[tc.code] {
			t.Errorf("duplicate code %q", tc.code)
		}
		seen[tc.code] = true
		checkErrorText(t, tc.name, tc.err)
	}
	got := declaredErrorNames(t)
	want := make([]string, 0, len(taxonomy))
	for _, tc := range taxonomy {
		want = append(want, tc.name)
	}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("declared error names = %v, want %v", got, want)
	}
}

// TestSentinelsAreDistinct asserts errors.Is tells every sentinel apart.
func TestSentinelsAreDistinct(t *testing.T) {
	for _, a := range taxonomy {
		for _, b := range taxonomy {
			if errors.Is(a.err, b.err) != (a.name == b.name) {
				t.Errorf("errors.Is(%s, %s) = %v", a.name, b.name, a.name != b.name)
			}
		}
	}
}

// checkErrorText asserts the message is one short line naming the code,
// so no stack trace or payload can ride along across a boundary.
func checkErrorText(t *testing.T, name string, err PortError) {
	t.Helper()
	msg := err.Error()
	if msg != "port: "+err.Code() {
		t.Errorf("%s.Error() = %q, want %q", name, msg, "port: "+err.Code())
	}
	if strings.ContainsAny(msg, "\n\t") {
		t.Errorf("%s.Error() spans lines: %q", name, msg)
	}
}

// declaredErrorNames parses the package's non-test sources and returns
// every package-level var named Err*, failing on any errors.New or
// fmt.Errorf call outside the taxonomy.
func declaredErrorNames(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, errVarNames(file)...)
		checkNoConstructors(t, f, file)
	}
	sort.Strings(names)
	return names
}

// errVarNames lists the package-level var names in file that start with Err.
func errVarNames(file *ast.File) []string {
	var names []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			for _, id := range spec.(*ast.ValueSpec).Names {
				if strings.HasPrefix(id.Name, "Err") {
					names = append(names, id.Name)
				}
			}
		}
	}
	return names
}

// checkNoConstructors fails on errors.New or fmt.Errorf anywhere in file.
func checkNoConstructors(t *testing.T, name string, file *ast.File) {
	t.Helper()
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if (pkg.Name == "errors" && sel.Sel.Name == "New") ||
			(pkg.Name == "fmt" && sel.Sel.Name == "Errorf") {
			t.Errorf("%s: %s.%s creates an error outside the taxonomy", name, pkg.Name, sel.Sel.Name)
		}
		return true
	})
}
