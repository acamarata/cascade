// Package build (this file) implements the AST clock/rand gate assigned to
// this ticket by R-14.132 (15-T0-RULINGS-R14.md): forbidigo's
// no-bare-clock/no-unseeded-rand rule (.golangci.yml) is a TEXT matcher —
// CR of A/S-01.T4 proved it fires for time.Now/time.Since/global math/rand
// but is defeated by an aliased import (`import t "time"`, then `t.Now()`)
// and by a dot-import, because forbidigo matches selector TEXT, not a
// resolved identifier. This is the exact evasion class R-14.120 already
// closed in the boundary lint (boundary_test.go); this gate applies the
// same fix here: resolve each file's import aliases (and reject dot-imports
// outright) before matching a call's selector against the denied set.
// forbidigo stays as the fast first line (fails on `go vet`-speed, no
// module compile needed); this gate is the durable proof, exactly as
// boundary_test.go is for the raw-error rule.
//
// # Scope and exemptions
//
// The denied set mirrors .golangci.yml's forbidigo forbid list exactly:
// time.Now, time.Since, and the seeded/unseeded math/rand draw functions.
// It does NOT add time.Tick/After/NewTimer/Unix — R-14.132 named those as
// an additional, separate forbidigo gap, but assigned THIS gate only the
// alias/dot-import evasion of the EXISTING forbidigo set; expanding the
// denied vocabulary itself is a policy change for whichever ticket owns
// .golangci.yml, not a silent scope-add here.
//
// Exemptions mirror forbidigo's own exclusions.rules: _test.go files (test
// code legitimately calls time.Now()/math/rand for tmp names or explicit
// non-determinism, Art.7.3) and the two canonical Clock implementations
// where bare time.Now() IS the sanctioned call
// (internal/runtime/clock.go, internal/testkit/clock.go).
package build

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
)

// clockgateDeniedCalls mirrors .golangci.yml's forbidigo forbid patterns
// for time and math/rand, keyed by "<resolved-package>.<func>" after
// import-alias resolution.
var clockgateDeniedCalls = map[string]bool{
	"time.Now":         true,
	"time.Since":       true,
	"rand.Int":         true,
	"rand.Int31":       true,
	"rand.Int31n":      true,
	"rand.Int63":       true,
	"rand.Int63n":      true,
	"rand.Intn":        true,
	"rand.Float32":     true,
	"rand.Float64":     true,
	"rand.Perm":        true,
	"rand.Shuffle":     true,
	"rand.Read":        true,
	"rand.NormFloat64": true,
	"rand.ExpFloat64":  true,
	"rand.Seed":        true,
}

// clockgateTrackedImports are the stdlib import paths this gate resolves
// aliases for and rejects dot-imports of.
var clockgateTrackedImports = map[string]bool{
	"time":      true,
	"math/rand": true,
}

// clockgateExemptSuffixes are the two canonical Clock implementations
// where bare time.Now() is the sanctioned call, mirrored from
// .golangci.yml's exclusions.rules.
var clockgateExemptSuffixes = []string{
	"internal/runtime/clock.go",
	"internal/testkit/clock.go",
}

// ClockViolation is one denied clock/rand call, or dot-import of time or
// math/rand, found via AST after alias resolution.
type ClockViolation struct {
	File string
	Line int
	Call string
}

// ClockgateIsExempt reports whether path (any absolute or root-relative
// form, forward- or back-slash) is a _test.go file or one of the two
// canonical Clock implementations.
func ClockgateIsExempt(path string) bool {
	slash := strings.ReplaceAll(path, "\\", "/")
	if strings.HasSuffix(slash, "_test.go") {
		return true
	}
	for _, suf := range clockgateExemptSuffixes {
		if strings.HasSuffix(slash, suf) {
			return true
		}
	}
	return false
}

// clockgateResolveImports builds the local-identifier -> real-import-path
// map for a file's "time" and "math/rand" imports, and separately reports
// any dot-import of either as an immediate violation — a dot-imported call
// is not even a *ast.SelectorExpr and so cannot be matched by selector at
// all, the same reasoning boundaryResolveImports documents.
func clockgateResolveImports(fset *token.FileSet, path string, file *ast.File) (map[string]string, []ClockViolation) {
	aliasToPkg := make(map[string]string)
	var violations []ClockViolation

	for _, imp := range file.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil || !clockgateTrackedImports[importPath] {
			continue
		}
		// The selector base identifier for "math/rand" (unaliased) is
		// "rand", matching Go's own default package-name resolution.
		defaultIdent := importPath
		if idx := strings.LastIndex(importPath, "/"); idx >= 0 {
			defaultIdent = importPath[idx+1:]
		}
		switch {
		case imp.Name == nil:
			aliasToPkg[defaultIdent] = defaultIdent
		case imp.Name.Name == "_":
			// Blank import: no call-site identifier to track.
		case imp.Name.Name == ".":
			violations = append(violations, ClockViolation{
				File: path,
				Line: fset.Position(imp.Pos()).Line,
				Call: "dot-import:" + importPath,
			})
		default:
			aliasToPkg[imp.Name.Name] = defaultIdent
		}
	}
	return aliasToPkg, violations
}

// ClockgateScanFile parses one Go source file and reports every denied
// time/rand call reached through its real package (resolved through
// whatever local alias the file's import declares) or a dot-import of
// time/math/rand.
func ClockgateScanFile(path string) ([]ClockViolation, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}

	aliasToPkg, out := clockgateResolveImports(fset, path, file)

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
		if !clockgateDeniedCalls[name] {
			return true
		}
		out = append(out, ClockViolation{
			File: path,
			Line: fset.Position(call.Pos()).Line,
			Call: name,
		})
		return true
	})
	return out, nil
}
