// Package build (this file) implements the dead-code gate (Art.10.5) and
// the pkg/ godoc gate (Art.10.6) from P1-E01-W1-S01-T8.
//
// # Why "unused" is a cross-file, position-based AST scan — not a
// text/grep heuristic (a real false-positive class, found by probing)
//
// A first draft counted a symbol "used" only if its bare name appeared in
// a DIFFERENT file than its declaration. Probed against this package's own
// real code, it immediately false-positived on the single most common Go
// idiom there is: `v := CheckCleanRoot(files)` never spells the return
// type `[]CleanRootViolation` again anywhere — the type is used entirely
// through inference — so a same-file-exclusion heuristic reports it as
// "unused" even though a function elsewhere returns it. The fix: track
// each declared symbol's own identifier POSITION (token.Pos) and count any
// OTHER occurrence of that bare name anywhere in the scanned tree
// (including the declaring file itself, including test files) as usage.
// Probed against the real tree with this fix, the finding count dropped
// from a false 16 (in this very package) to the true zero. This is still
// not perfect type-aware dead-code analysis (that needs go/types with full
// import resolution, effectively golang.org/x/tools/go/packages — an
// external dependency this ticket is not authorized to add, R-14.115): a
// symbol referenced only through struct embedding without ever naming its
// type, or shadowed by an unrelated identically-named local variable
// nearby, can still evade or falsely trip a syntactic scan. That
// remaining gap is named, not hidden.
package build

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// DeadCodeSymbol identifies one top-level exported symbol (function, type,
// or package-level const/var — methods are excluded: a method is reached
// through its receiver type, which this scan already tracks, and Go's own
// compiler already flags a truly-unreachable unexported method's
// unused-ness where it can).
type DeadCodeSymbol struct {
	Dir  string // module-relative package directory, e.g. "internal/build"
	Name string
}

// DeadCodeDecl is one declared symbol's source location and godoc text
// (Doc is the raw comment group text, empty if undocumented).
type DeadCodeDecl struct {
	File string
	Line int
	Doc  string
}

// ScanModuleSymbols walks each of roots (module-relative directory names,
// e.g. "cmd", "internal") under moduleRoot and returns every exported
// top-level symbol declared in a non-test .go file (declared), and the set
// of symbols referenced ANYWHERE in the scanned tree — same package bare
// identifiers and cross-package qualified selectors alike, in both
// shipped and _test.go files (used). testdata/ subtrees are always
// skipped.
func ScanModuleSymbols(moduleRoot, modulePath string, roots []string) (declared map[DeadCodeSymbol]DeadCodeDecl, used map[DeadCodeSymbol]bool, err error) {
	files, err := deadcodeCollectFiles(moduleRoot, roots)
	if err != nil {
		return nil, nil, err
	}

	fset := token.NewFileSet()
	parsed := make(map[string]*ast.File, len(files))
	for _, path := range files {
		f, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			return nil, nil, perr
		}
		parsed[path] = f
	}

	declared, declPos, err := deadcodeCollectDeclared(moduleRoot, fset, parsed)
	if err != nil {
		return nil, nil, err
	}
	used, err = deadcodeCollectUsed(moduleRoot, modulePath, parsed, declPos)
	if err != nil {
		return nil, nil, err
	}
	return declared, used, nil
}

// DeadCodeViolation is one exported internal/ symbol found nowhere in the
// scanned tree (Art.10.5, hard-fail half).
type DeadCodeViolation struct {
	Dir  string
	Name string
	File string
	Line int
}

// InternalDeadCodeViolations filters declared/used down to internal/**
// symbols with zero usage (Art.10.5: "unused exported symbols in
// internal/ fail CI").
func InternalDeadCodeViolations(declared map[DeadCodeSymbol]DeadCodeDecl, used map[DeadCodeSymbol]bool) []DeadCodeViolation {
	var out []DeadCodeViolation
	for sym, decl := range declared {
		if sym.Dir != "internal" && !strings.HasPrefix(sym.Dir, "internal/") {
			continue
		}
		if used[sym] {
			continue
		}
		out = append(out, DeadCodeViolation{Dir: sym.Dir, Name: sym.Name, File: decl.File, Line: decl.Line})
	}
	return out
}

// sdkIntentMarker is the godoc token that justifies an unused pkg/ symbol
// per Art.10.5's second half ("unused pkg/ symbols require an SDK-intent
// note") — a documented, deliberate forward-declared public API surface,
// as distinct from genuine dead code.
const sdkIntentMarker = "SDK-INTENT"

// SDKIntentViolation is one unused pkg/ symbol whose godoc does not carry
// the SDK-intent marker.
type SDKIntentViolation struct {
	Dir  string
	Name string
	File string
	Line int
}

// PkgUnusedWithoutSDKIntent filters declared/used down to pkg/** symbols
// with zero usage AND no SDK-intent marker in their godoc.
func PkgUnusedWithoutSDKIntent(declared map[DeadCodeSymbol]DeadCodeDecl, used map[DeadCodeSymbol]bool) []SDKIntentViolation {
	var out []SDKIntentViolation
	for sym, decl := range declared {
		if sym.Dir != "pkg" && !strings.HasPrefix(sym.Dir, "pkg/") {
			continue
		}
		if used[sym] {
			continue
		}
		if strings.Contains(decl.Doc, sdkIntentMarker) {
			continue
		}
		out = append(out, SDKIntentViolation{Dir: sym.Dir, Name: sym.Name, File: decl.File, Line: decl.Line})
	}
	return out
}
