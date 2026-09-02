// Package build holds build-time gates that assert properties of the repo
// tree itself rather than of runtime behavior. This file implements the
// boundary lint from P1-E01-W1-S01-T7: exported API surfaces (pkg/, cmd/
// composition) must return pkg/cascade taxonomy errors, never a raw
// fmt.Errorf/errors.New value — internal/ packages remain free to wrap
// however they like (12-QUALITY-CONSTITUTION.md Art.10.2).
//
// The gate is intentionally implemented entirely inside this _test.go file:
// T-7's file scope permits only boundary_test.go in this package (a
// concurrently dispatched ticket owns arch_test.go, desktop_test.go, and
// filecap_test.go here), and `go test ./internal/build/...` — one of T-7's
// contract checks — is what runs it.
package build

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

// boundaryViolation is one raw-error-constructor call found at a scanned
// boundary.
type boundaryViolation struct {
	file string
	line int
	call string // "fmt.Errorf" or "errors.New"
}

// moduleRoot locates the repo root (the directory holding go.mod) by
// walking up from this source file's own path, so the gate works regardless
// of the working directory `go test` is invoked from.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("boundary lint: runtime.Caller(0) failed; cannot locate module root")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("boundary lint: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

// scanBoundary walks each root looking for calls to fmt.Errorf or
// errors.New in non-test .go files, skipping testdata/ subtrees (those hold
// fixtures, not shipped code, and testdata/ is already skipped by the Go
// toolchain itself). A root that does not exist yet is silently skipped —
// early waves may not have populated cmd/ or pkg/ fully.
func scanBoundary(t *testing.T, roots ...string) []boundaryViolation {
	t.Helper()
	var out []boundaryViolation

	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			out = append(out, scanFile(t, path)...)
			return nil
		})
		if walkErr != nil {
			t.Fatalf("boundary lint: walking %s: %v", root, walkErr)
		}
	}
	return out
}

// scanFile parses one Go source file and reports every fmt.Errorf/
// errors.New call it contains.
func scanFile(t *testing.T, path string) []boundaryViolation {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("boundary lint: parsing %s: %v", path, err)
	}

	var out []boundaryViolation
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		name := pkgIdent.Name + "." + sel.Sel.Name
		if name != "fmt.Errorf" && name != "errors.New" {
			return true
		}
		out = append(out, boundaryViolation{
			file: path,
			line: fset.Position(call.Pos()).Line,
			call: name,
		})
		return true
	})
	return out
}

// TestBoundaryLint_RealTree is the passing case (AC: "passes on the real
// tree"): the actual pkg/ and cmd/ trees must contain zero raw
// fmt.Errorf/errors.New calls. pkg/cascade itself is in-scope here — the
// taxonomy package must obey its own rule.
func TestBoundaryLint_RealTree(t *testing.T) {
	root := moduleRoot(t)
	violations := scanBoundary(t, filepath.Join(root, "pkg"), filepath.Join(root, "cmd"))
	if len(violations) == 0 {
		return
	}
	var b strings.Builder
	for _, v := range violations {
		b.WriteString(v.file)
		b.WriteString(": raw ")
		b.WriteString(v.call)
		b.WriteString(" at a pkg/cmd boundary; return a pkg/cascade taxonomy error instead\n")
	}
	t.Fatalf("boundary lint found %d violation(s) in the real tree:\n%s", len(violations), b.String())
}

// TestBoundaryLint_SeededViolation is the failing case (AC: "fails on its
// seeded raw-error boundary fixture"): the fixture under
// testdata/seeded-violations/boundary/ deliberately contains both raw-
// constructor forms, and the scanner must find both.
func TestBoundaryLint_SeededViolation(t *testing.T) {
	root := moduleRoot(t)
	fixture := filepath.Join(root, "internal", "build", "testdata", "seeded-violations", "boundary")
	violations := scanBoundary(t, fixture)
	if len(violations) == 0 {
		t.Fatalf("boundary lint: expected violations in seeded fixture %s, found none", fixture)
	}

	var sawErrorf, sawErrorsNew bool
	for _, v := range violations {
		switch v.call {
		case "fmt.Errorf":
			sawErrorf = true
		case "errors.New":
			sawErrorsNew = true
		}
	}
	if !sawErrorf {
		t.Error("seeded fixture: expected at least one fmt.Errorf violation, found none")
	}
	if !sawErrorsNew {
		t.Error("seeded fixture: expected at least one errors.New violation, found none")
	}
}
