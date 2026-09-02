// Package build holds build-time gates that assert properties of the repo
// tree itself rather than of runtime behavior. This file implements the
// boundary lint from P1-E01-W1-S01-T7: exported API surfaces (pkg/, cmd/
// composition) must return pkg/cascade taxonomy errors, never a raw
// fmt.Errorf/errors.New/errors.Join value — internal/ packages remain free
// to wrap however they like (12-QUALITY-CONSTITUTION.md Art.10.2).
//
// The gate is intentionally implemented entirely inside this _test.go file:
// T-7's file scope permits only boundary_test.go in this package (a
// concurrently dispatched ticket owns arch_test.go, desktop_test.go, and
// filecap_test.go here), and `go test ./internal/build/...` — one of T-7's
// contract checks — is what runs it. Its seeded-violation counterparts live
// in the sibling boundary_seeded_test.go (R-14.117 split), authorized by
// R-14.120's hardening pass.
//
// # What this gate proves, and what it does not (R-14.120)
//
// This is an AST scan of pkg/ and cmd/ non-test .go files. It resolves each
// file's import aliases before matching a call's selector, so it catches:
//   - a literal fmt.Errorf(...) / errors.New(...) / errors.Join(...) call;
//   - the same call reached through an import alias, e.g.
//     `import ferrors "fmt"` then `ferrors.Errorf(...)`;
//   - a dot-import of "fmt" or "errors" in a scanned tree, which is rejected
//     outright at the import declaration regardless of how the imported
//     names are used, since a dot-imported call is not even a
//     *ast.SelectorExpr and so cannot be matched by selector at all.
//
// It does NOT and cannot prove the following, by construction of what an
// AST scan of pkg/ and cmd/ can see:
//   - a raw error minted inside internal/ (with fmt.Errorf/errors.New/
//     errors.Join, all of which internal/ is free to use) and returned
//     unchanged through a pkg/ or cmd/ boundary — the raw-error-constructor
//     call itself is not textually present in the scanned trees, only its
//     already-built error.Error value flowing through a return statement,
//     which this scanner does not attempt to trace across package
//     boundaries;
//   - a bespoke type that implements the `error` interface directly (no
//     fmt/errors constructor call anywhere), which produces no AST node
//     this lint watches for at all.
//
// Per R-14.118, closing that second class is the responsibility of the
// ticket that owns each pkg/cmd boundary (via cascade.Wrap at the point
// where an internal/ error crosses out), not of this lint. This gate proves
// "no raw constructor call written directly in pkg/ or cmd/ source", not
// "no raw error value can reach a caller of pkg/ or cmd/". Both statements
// are documented identically in docs/reference/error-taxonomy.md so a later
// ticket does not mistake the narrower claim for the broader one.
package build

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// boundaryViolation is one denied-construct finding at a scanned boundary:
// either a raw-error-constructor call (call is "fmt.Errorf", "errors.New",
// or "errors.Join") or a dot-import of "fmt"/"errors" (call is
// "dot-import:fmt" or "dot-import:errors").
type boundaryViolation struct {
	file string
	line int
	call string
}

// boundaryDeniedCalls is the set of raw-error-constructor selectors the
// lint denies at a pkg/cmd boundary, keyed by "<resolved-package>.<func>"
// after import-alias resolution (boundaryResolveImports). errors.Join was
// added by R-14.120(b): it mints a raw, kindless error exactly like
// errors.New and fmt.Errorf and was previously an unchecked gap.
var boundaryDeniedCalls = map[string]bool{
	"fmt.Errorf":  true,
	"errors.New":  true,
	"errors.Join": true,
}

// boundaryDeniedDotImports is the set of stdlib import paths that may never
// be dot-imported in a scanned tree (R-14.120(a)): a dot-import makes every
// exported name of that package callable unqualified, so
// `import . "errors"` then `New(...)` is not even a *ast.SelectorExpr and
// defeats selector-based matching entirely. Rejected outright at the import
// declaration, independent of whether the dot-imported names are actually
// called.
var boundaryDeniedDotImports = map[string]bool{
	"fmt":    true,
	"errors": true,
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

// scanBoundary walks each root looking for denied raw-error-constructor
// calls and denied dot-imports in non-test .go files, skipping testdata/
// subtrees (those hold fixtures, not shipped code, and testdata/ is already
// skipped by the Go toolchain itself). A root that does not exist yet is
// silently skipped — early waves may not have populated cmd/ or pkg/ fully.
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

// scanFile parses one Go source file and reports every denied
// raw-error-constructor call or denied dot-import it contains. Import
// aliases are resolved first (boundaryResolveImports) so the selector match
// below sees the real package, not the local alias — this closes
// R-14.120(a)'s `import ferrors "fmt"` evasion.
func scanFile(t *testing.T, path string) []boundaryViolation {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("boundary lint: parsing %s: %v", path, err)
	}

	aliasToPkg, out := boundaryResolveImports(fset, path, file)

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
		resolved, tracked := aliasToPkg[pkgIdent.Name]
		if !tracked {
			return true
		}
		name := resolved + "." + sel.Sel.Name
		if !boundaryDeniedCalls[name] {
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

// boundaryResolveImports builds the local-identifier -> real-import-path
// map for a file's "fmt" and "errors" imports (so aliased imports resolve
// to the package they actually name), and separately reports any dot-import
// of "fmt"/"errors" as an immediate violation (R-14.120(a)). A blank import
// (`_ "fmt"`) is not trackable as a call-site identifier and is skipped, as
// is any import unrelated to fmt/errors.
func boundaryResolveImports(fset *token.FileSet, path string, file *ast.File) (map[string]string, []boundaryViolation) {
	aliasToPkg := make(map[string]string)
	var violations []boundaryViolation

	for _, imp := range file.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil || (importPath != "fmt" && importPath != "errors") {
			continue
		}
		switch {
		case imp.Name == nil:
			aliasToPkg[importPath] = importPath
		case imp.Name.Name == "_":
			// Blank import: no call-site identifier to track.
		case imp.Name.Name == ".":
			if boundaryDeniedDotImports[importPath] {
				violations = append(violations, boundaryViolation{
					file: path,
					line: fset.Position(imp.Pos()).Line,
					call: "dot-import:" + importPath,
				})
			}
		default:
			aliasToPkg[imp.Name.Name] = importPath
		}
	}
	return aliasToPkg, violations
}

// TestBoundaryLint_RealTree is the passing case (AC: "passes on the real
// tree"): the actual pkg/ and cmd/ trees must contain zero raw
// fmt.Errorf/errors.New/errors.Join calls and zero dot-imports of fmt or
// errors. pkg/cascade itself is in-scope here — the taxonomy package must
// obey its own rule.
func TestBoundaryLint_RealTree(t *testing.T) {
	root := moduleRoot(t)
	violations := scanBoundary(t, filepath.Join(root, "pkg"), filepath.Join(root, "cmd"))
	if len(violations) == 0 {
		return
	}
	var b strings.Builder
	for _, v := range violations {
		b.WriteString(v.file)
		b.WriteString(": ")
		if strings.HasPrefix(v.call, "dot-import:") {
			b.WriteString("dot-imports ")
			b.WriteString(strings.TrimPrefix(v.call, "dot-import:"))
			b.WriteString(" at a pkg/cmd boundary; import it normally (or aliased) instead\n")
			continue
		}
		b.WriteString("raw ")
		b.WriteString(v.call)
		b.WriteString(" at a pkg/cmd boundary; return a pkg/cascade taxonomy error instead\n")
	}
	t.Fatalf("boundary lint found %d violation(s) in the real tree:\n%s", len(violations), b.String())
}
