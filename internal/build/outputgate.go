// Package build (this file) implements the output-seam AST gate assigned by
// R-14.137 (15-T0-RULINGS-R14.md) to P1-E01-W1-S01-T8, closing an omission
// R-14.140 records: R-14.137 was written after T8 was dispatched and the
// running agent was never told, so T8 shipped without it.
//
// The seam: nothing outside internal/output may write to the process's real
// stdout/stderr — internal/output.Writer is the only sanctioned path,
// because the CLI's human/JSON/quiet modes and its versioned envelope all
// flow through it (internal/output/output.go's NewDefault doc comment).
// The existing .golangci.yml forbidigo rule (D/S-06.T5) enforces a narrower
// version of this — scoped to cmd/** only — and CR of D/S-06.T5 proved it
// defeated two non-exotic ways: an aliased import (`osalias "os"`, then
// `osalias.Stdout`) reports zero forbidigo issues, because forbidigo
// matches selector TEXT, not a resolved identifier (the same root cause
// R-14.120 and R-14.132 already fixed elsewhere in internal/build); and ANY
// indirection through a helper package outside cmd/** evades it entirely,
// since the rule is per-file text matching scoped to one directory.
//
// This gate follows clockgate.go's established shape (itself R-14.132's
// fix for the identical evasion class): resolve each file's import aliases
// before matching, reject dot-imports of the tracked packages outright, and
// prove RED/GREEN on seeded fixtures rather than asserting correctness.
// forbidigo STAYS as the fast first line (R-14.132/R-14.137 both say so);
// this gate is the durable, whole-program proof.
//
// # Denied set
//
// os.Stdout and os.Stderr are DATA references, not calls — unlike
// clockgate's time.Now/rand.Intn, a program can pass os.Stdout as a plain
// argument (`fmt.Fprintln(os.Stdout, ...)`) without ever "calling" it. So,
// unlike clockgate/boundary_test.go (which only match a selector when it is
// specifically a CallExpr's Fun), this gate matches the resolved selector
// text ANYWHERE it appears in the AST — the same breadth forbidigo itself
// needs to catch `os.Stdout` as a bare argument. fmt.Print/Println/Printf
// are matched the same general way (not restricted to being the immediate
// Fun of a CallExpr), since a reference to the bare function value
// (`p := fmt.Println`) is the same evasion shape as a variable capture and
// is cheap to also catch.
//
// fmt.Fprint/Fprintln/Fprintf are NOT denied outright — writing to a real
// io.Writer (a file, a buffer, an internal/output.Writer) is exactly the
// sanctioned pattern. R-14.137 asks whether Fprint* TARGETING os.Stdout/
// os.Stderr as its first argument should be caught: yes, and it already is,
// for free — `fmt.Fprintln(os.Stdout, x)` contains an `os.Stdout` selector
// node as Args[0], which the general os.Stdout/os.Stderr match above finds
// regardless of which call it sits inside. A dedicated "Fprint* whose first
// arg is os.Stdout" rule would be strictly narrower than, and redundant
// with, the general selector match: it can only fire in exactly the cases
// the general rule already requires. No separate rule was added.
//
// # What this proves, and what it does not (same honesty standard as
// clockgate.go and boundary_test.go)
//
// Scanning EVERY non-exempt file under cmd/, internal/, pkg/, providers/,
// and plugins/ — not only cmd/ — converts R-14.137's whole-program property
// ("nothing outside internal/output writes to the real streams") into a
// per-file one at the DEFINITION site: a helper in some other package that
// writes to os.Stdout directly is caught where it is written, even though
// the call site that reaches it (e.g. from cmd/) contains no os/fmt
// reference at all and would look innocent to a cmd/-only scan.
// TestOutputGate_SeededViolationRed_CrossPackageHelper proves exactly this
// shape: a helper file with a direct os.Stdout write is RED, and the
// separate caller file that only calls the helper is GREEN, in the same
// scan.
//
// It does NOT and cannot prove the following, by construction of a
// per-file AST scan with no dataflow analysis:
//   - os.Stdout/os.Stderr captured into a variable, struct field, or
//     function parameter and read back later — `w := os.Stdout` followed
//     by `w.Write(...)` elsewhere breaks the direct-selector match, because
//     the read site names only the local variable `w`, never `os.Stdout`
//     again. Closing this needs real dataflow/type-aware analysis
//     (effectively golang.org/x/tools/go/packages + SSA), a dependency
//     this ticket is not authorized to add (R-14.115).
//   - a file descriptor obtained by any route other than the os.Stdout/
//     os.Stderr identifiers themselves (e.g. os.NewFile(1, ...), or a
//     third-party/vendored dependency's own direct stdout write) — outside
//     this module's source tree, or simply not spelled the denied way.
//   - the earlier boundary/clockgate gap already named for this repo's
//     class of AST scan: a bespoke io.Writer type whose Write method
//     itself, ultimately, forwards to a captured os.Stdout — same
//     dataflow limitation as above, one layer further removed.
//
// These are recorded here, in docs/developer/quality-gates.md, and were
// never claimed closed — R-14.132/R-14.137 both flag that overclaiming a
// gate's scope is worse than a narrower gate honestly described; this repo
// has already found three rules that looked correct and enforced nothing.
//
// # Exemptions (deliberately narrow, each named)
//
//  1. _test.go files — test code may legitimately capture os.Stdout/
//     os.Stderr (e.g. to redirect them under a subprocess harness) or use
//     fmt.Println for local debugging during development.
//  2. internal/output/**  — the sanctioned Writer package itself; New/
//     NewDefault in output.go are the ONE place os.Stdout/os.Stderr are
//     meant to be named.
//  3. plugins/examples/** — R-14.137/D/S-06.T5 both record that an earlier
//     repo-wide forbidigo attempt caught plugins/examples' standalone
//     example plugin (plugins/examples/example-builtin/plugin.go's
//     fmt.Println), which legitimately prints on its own account to
//     demonstrate a plugin's own I/O, independent of the cascade CLI's
//     output contract. Scoped to plugins/examples/** only — a real,
//     first-party plugin anywhere else in plugins/** is still gated.
package build

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
)

// outputgateTrackedImports are the stdlib import paths this gate resolves
// aliases for and rejects dot-imports of.
var outputgateTrackedImports = map[string]bool{
	"os":  true,
	"fmt": true,
}

// outputgateDeniedSelectors is the set of denied "<resolved-package>.<name>"
// selectors, matched anywhere they occur in the AST (not only as a call's
// Fun — see the package doc for why os.Stdout/os.Stderr need that breadth).
var outputgateDeniedSelectors = map[string]bool{
	"os.Stdout":   true,
	"os.Stderr":   true,
	"fmt.Print":   true,
	"fmt.Println": true,
	"fmt.Printf":  true,
}

// outputgateExemptDirs are the two directory trees this gate never scans,
// named individually in the package doc's Exemptions section.
var outputgateExemptDirs = []string{
	"internal/output",
	"plugins/examples",
}

// OutputViolation is one denied stdout/stderr reference, denied bare-print
// call, or dot-import of os/fmt, found via AST after alias resolution.
type OutputViolation struct {
	File string
	Line int
	Call string
}

// OutputgateIsExempt reports whether path (any absolute or root-relative
// form, forward- or back-slash) is a _test.go file or falls under one of
// outputgateExemptDirs.
func OutputgateIsExempt(path string) bool {
	slash := strings.ReplaceAll(path, "\\", "/")
	if strings.HasSuffix(slash, "_test.go") {
		return true
	}
	for _, dir := range outputgateExemptDirs {
		if strings.Contains(slash, "/"+dir+"/") || strings.HasPrefix(slash, dir+"/") {
			return true
		}
	}
	return false
}

// outputgateResolveImports builds the local-identifier -> real-import-path
// map for a file's "os" and "fmt" imports, and separately reports any
// dot-import of either as an immediate violation — a dot-imported call is
// not even a *ast.SelectorExpr and so cannot be matched by selector at all
// (the same reasoning clockgateResolveImports and boundaryResolveImports
// document).
func outputgateResolveImports(fset *token.FileSet, path string, file *ast.File) (map[string]string, []OutputViolation) {
	aliasToPkg := make(map[string]string)
	var violations []OutputViolation

	for _, imp := range file.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil || !outputgateTrackedImports[importPath] {
			continue
		}
		switch {
		case imp.Name == nil:
			aliasToPkg[importPath] = importPath
		case imp.Name.Name == "_":
			// Blank import: no call-site identifier to track.
		case imp.Name.Name == ".":
			violations = append(violations, OutputViolation{
				File: path,
				Line: fset.Position(imp.Pos()).Line,
				Call: "dot-import:" + importPath,
			})
		default:
			aliasToPkg[imp.Name.Name] = importPath
		}
	}
	return aliasToPkg, violations
}

// OutputgateScanFile parses one Go source file and reports every denied
// os.Stdout/os.Stderr reference, denied bare fmt.Print/Println/Printf
// reference, or dot-import of os/fmt — each reached through its real
// package, resolved through whatever local alias the file's import
// declares.
func OutputgateScanFile(path string) ([]OutputViolation, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}

	aliasToPkg, out := outputgateResolveImports(fset, path, file)

	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
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
		if !outputgateDeniedSelectors[name] {
			return true
		}
		out = append(out, OutputViolation{
			File: path,
			Line: fset.Position(sel.Pos()).Line,
			Call: name,
		})
		return true
	})
	return out, nil
}
