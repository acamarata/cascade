// Package build (this file) closes R-14.155's gap in coveragegate.go's
// gate: a package whose TEST BINARY FAILS TO COMPILE (or panics before
// `go test -coverprofile` ever gets to write its package's line) emits NO
// profile entry at all. CheckCoverage can only judge packages the profile
// MENTIONS — a package that disappears from the profile disappears from
// its violation list too, and a run with a truncated profile can report
// FEWER violations than a healthy one, which reads as progress when it is
// the opposite. This file adds a second, independent check: profile
// COMPLETENESS. Every package PackageTier assigns an Art.4 floor to, that
// is ALSO buildable under DEFAULT tags (no -tags=..., matching exactly
// what the coverage lane's own `go test ./...` compiles), must be PRESENT
// in the profile; one that is not is a violation in its own right (Reason
// "missing"), never a silent absence.
//
// # "Buildable under default tags" is DERIVED, never a name list
//
// A package entirely gated behind a build tag (providers/postgres's
// `//go:build postgres`) can never appear in a default-tag profile — that
// is its normal, permanent state, not a defect, and it must never be
// expected. The distinction that matters is not "is this package named
// providers/postgres" (a hardcoded exemption the next tag-gated package
// would silently evade) and not "does this package measure 0%" (that
// readmits the exact bug this file exists to close: a package whose tests
// fail to compile also measures nothing, and must NOT be waved through).
// The real distinction is: would `go build`/`go test` compile ANY of this
// directory's files with no extra tags, on this host? DiscoverPackages
// answers that with go/build.ImportDir — the standard library's own
// build-constraint evaluator, the same logic `go list`/`go build` use —
// never a textual "does this file mention //go:build" heuristic (which
// would wrongly exclude a file like lifecycle_unix.go's `//go:build
// !windows`, which DOES build by default on this host) and never a
// maintained exemption list.
//
// # Where the "packages that should be present" list comes from
//
// DiscoverPackages walks the checked-out source tree itself (cmd/,
// internal/, pkg/, providers/, plugins/) and returns the module-relative
// import path of every directory holding at least one DEFAULT-BUILDABLE
// non-test .go file with real declarations (not just a doc.go placeholder
// — see discoverDirHasShippableGoFile). Two alternatives were considered
// and rejected:
//
//   - A maintained list (a JSON/YAML file enumerating every package, like
//     coverage-baseline.json does for baselines): this is exactly the
//     kind of list that goes stale — a new package, or a newly tag-gated
//     one, added without a matching list entry would be invisible to the
//     completeness check the same way a compile failure makes it
//     invisible to CheckCoverage, defeating the point.
//   - Shelling out to `go list ./...`: this repo's own environment notes
//     (R-14.114) record that `go` is rtk-wrapped in the interactive dev
//     environment this gate is developed and CI-verified from, and that
//     wrapping silently drops flags and misreports exit codes; a gate
//     that shells out to a name as ambient as "go" inherits that risk for
//     anyone running it locally. go/build.ImportDir is the same standard-
//     library logic `go list` runs internally, in-process, with no
//     subprocess and nothing to intercept.
//
// This is therefore the only source that is BOTH always current (it reads
// the same tree, under the same host's default build constraints, the
// coverage run just compiled) and immune to shell/PATH interference — not
// brittle, because there is nothing to keep in sync by hand.
package build

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// discoverPackageRoots are the five directory trees this gate's package
// doc (coveragegate.go) already names as PackageTier's scanned universe.
// pkg/** is walked too (so a stray pkg/** entry can be reasoned about) but
// PackageTier excludes it from every floor, so CheckCompleteness below
// never reports it missing.
var discoverPackageRoots = []string{"cmd", "internal", "pkg", "providers", "plugins"}

// discoverSkipDirName reports directory names DiscoverPackages never
// descends into: dotdirs (.git, .claude, …), "_"-prefixed scratch dirs (Go
// tooling's own convention for "ignore me"), and testdata (fixtures, never
// a real importable package).
func discoverSkipDirName(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata"
}

// DiscoverPackages returns the sorted, module-relative import path of
// every directory under root's five scanned trees that is buildable under
// DEFAULT tags and has at least one non-test .go file with real
// declarations — this gate's source of "packages a default-tag coverage
// profile could ever mention", used to detect one the profile is missing
// entirely (R-14.155). A tree that does not exist yet (e.g. a fresh
// checkout before plugins/ has any content) is skipped, not an error.
func DiscoverPackages(root string) ([]string, error) {
	var out []string
	for _, treeName := range discoverPackageRoots {
		base := filepath.Join(root, treeName)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		if err := discoverWalkTree(root, base, &out); err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

// discoverWalkTree walks one root tree (base), appending root-relative,
// slash-form import paths for every directory with a default-buildable,
// non-placeholder .go file.
func discoverWalkTree(modRoot, base string, out *[]string) error {
	return filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if path != base && discoverSkipDirName(d.Name()) {
			return filepath.SkipDir
		}
		hasSource, statErr := discoverDirHasShippableGoFile(path)
		if statErr != nil {
			return statErr
		}
		if !hasSource {
			return nil
		}
		rel, relErr := filepath.Rel(modRoot, path)
		if relErr != nil {
			return relErr
		}
		*out = append(*out, filepath.ToSlash(rel))
		return nil
	})
}

// discoverDirHasShippableGoFile reports whether dir is a real, expected
// package: it must (1) be buildable under DEFAULT tags — at least one
// non-test .go file survives go/build's own constraint evaluation with no
// extra tags, on this host, exactly like `go build`/`go test ./...` would
// compile it — and (2) at least one of those default-buildable files must
// declare something beyond a bare "package foo" clause plus a doc
// comment (a doc.go-only scaffold for a future wave's not-yet-built
// package). Both conditions are DERIVED from the files themselves, never
// a name list:
//
//   - (1) rules providers/postgres (`//go:build postgres`, no default
//     tags ever satisfy it) out cleanly, and rules internal/daemon's
//     lifecycle_unix.go (`//go:build !windows`, satisfied by default on
//     this host) IN — a plain "does this file mention //go:build"
//     heuristic would get the second case wrong.
//   - (2) rules ~15 doc.go-only future-wave stubs (internal/audit,
//     internal/secrets, …) out; CheckCoverage's own doc comment already
//     establishes this exemption for a package the profile DOES mention
//     at zero total statements ("nothing to measure, and Art.4 floors
//     govern shipped behaviour, not empty packages") — this extends the
//     same test to a package the profile does not mention at all.
func discoverDirHasShippableGoFile(dir string) (bool, error) {
	pkg, err := build.ImportDir(dir, build.IgnoreVendor)
	if err != nil {
		if _, isNoGo := err.(*build.NoGoError); isNoGo {
			// Nothing in dir builds under default tags — e.g. every file
			// is behind a tag like `//go:build postgres`. There was
			// nothing to look at, never a "missing" violation.
			return false, nil
		}
		return false, fmt.Errorf("coverage gate: go/build.ImportDir(%s): %w", dir, err)
	}
	fset := token.NewFileSet()
	for _, name := range pkg.GoFiles {
		shippable, declErr := fileHasNonImportDecl(fset, filepath.Join(dir, name))
		if declErr != nil {
			return false, declErr
		}
		if shippable {
			return true, nil
		}
	}
	return false, nil
}

// fileHasNonImportDecl parses path and reports whether it declares
// anything beyond an import block: any FuncDecl, or any GenDecl whose
// token is not IMPORT (var, const, type). A file with none of those —
// just "package x" and comments, this repo's doc.go convention for a
// package nobody has started building yet — reports false. path has
// already passed go/build's default-tag filter (discoverDirHasShippableGoFile
// only calls this for names in pkg.GoFiles), so this function does no
// build-constraint evaluation of its own.
func fileHasNonImportDecl(fset *token.FileSet, path string) (bool, error) {
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return false, fmt.Errorf("coverage gate: parsing %s: %w", path, err)
	}
	for _, decl := range file.Decls {
		if _, isFunc := decl.(*ast.FuncDecl); isFunc {
			return true, nil
		}
		if gen, isGen := decl.(*ast.GenDecl); isGen && gen.Tok != token.IMPORT {
			return true, nil
		}
	}
	return false, nil
}

// CheckCompleteness reports one CoverageViolation{Reason: "missing"} for
// every package in expected (module-relative import paths, e.g.
// DiscoverPackages' return value) that PackageTier assigns an Art.4 floor
// to but that is entirely ABSENT from profile — the R-14.155 failure mode:
// a test binary that fails to compile (or panics before the profile is
// written) leaves its package with no profile entry at all, which
// CheckCoverage's floor/ratchet logic can never see, because it only
// judges packages the profile mentions. A package the profile DOES
// mention (even at 0 total statements) is not "missing" — that is
// CheckCoverage's "nothing to measure" case, a different, legitimate
// state this function does not re-adjudicate. expected already excludes
// anything DiscoverPackages found not to be default-tag-buildable, so
// this function itself never re-examines build constraints or measured
// coverage — presence in profile is the only question it asks.
func CheckCompleteness(profile map[string]*CoverageStats, expected []string) []CoverageViolation {
	var out []CoverageViolation
	for _, pkg := range expected {
		_, floor, ok := PackageTier(pkg)
		if !ok {
			continue
		}
		if _, present := profile[pkg]; present {
			continue
		}
		out = append(out, CoverageViolation{Package: pkg, Floor: floor, Reason: "missing"})
	}
	return out
}
