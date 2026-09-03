// Package build (this file) closes R-14.155's gap in coveragegate.go's
// gate: a package whose TEST BINARY FAILS TO COMPILE (or panics before
// `go test -coverprofile` ever gets to write its package's line) emits NO
// profile entry at all. CheckCoverage can only judge packages the profile
// MENTIONS — a package that disappears from the profile disappears from
// its violation list too, and a run with a truncated profile can report
// FEWER violations than a healthy one, which reads as progress when it is
// the opposite. This file adds a second, independent check: profile
// COMPLETENESS. Every package PackageTier assigns an Art.4 floor to must
// be PRESENT in the profile; one that is not is a violation in its own
// right (Reason "missing"), never a silent absence.
//
// # Where the "packages that should be present" list comes from
//
// DiscoverPackages walks the checked-out source tree itself (cmd/,
// internal/, pkg/, providers/, plugins/) and returns the module-relative
// import path of every directory holding at least one non-test .go file.
// Two alternatives were considered and rejected:
//
//   - A maintained list (a JSON/YAML file enumerating every package, like
//     coverage-baseline.json does for baselines): this is exactly the
//     kind of list that goes stale — a new package added without a
//     matching list entry would be invisible to the completeness check
//     the same way a compile failure makes it invisible to CheckCoverage,
//     defeating the point.
//   - Shelling out to `go list ./...`: this repo's own environment notes
//     (R-14.114) record that `go` is rtk-wrapped in the interactive dev
//     environment this gate is developed and CI-verified from, and that
//     wrapping silently drops flags and misreports exit codes; a gate
//     that shells out to a name as ambient as "go" inherits that risk
//     for anyone running it locally. A filesystem walk needs no
//     subprocess and cannot be intercepted this way.
//
// The filesystem walk is therefore the only source that is BOTH always
// current (it reads the same tree the coverage run just compiled) and
// immune to shell/PATH interference — not brittle, because there is
// nothing to keep in sync by hand.
package build

import (
	"fmt"
	"go/ast"
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

// discoverSkipDirNames are directory names DiscoverPackages never
// descends into: dotdirs (.git, .claude, …), "_"-prefixed scratch dirs (Go
// tooling's own convention for "ignore me"), and testdata (fixtures, never
// a real importable package).
func discoverSkipDirName(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata"
}

// DiscoverPackages returns the sorted, module-relative import path of
// every directory under root's five scanned trees that contains at least
// one non-test .go file — this gate's source of "packages that exist",
// used to detect a package the coverage profile is missing entirely
// (R-14.155). A tree that does not exist yet (e.g. a fresh checkout before
// plugins/ has any content) is skipped, not an error.
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
// slash-form import paths for every directory with a non-test .go file.
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

// discoverDirHasShippableGoFile reports whether dir directly contains
// (non-recursively) at least one non-test .go file with real declarations
// — not just a "package foo" clause plus a doc comment (a doc.go-only
// scaffold for a future wave's not-yet-built package). CheckCoverage's own
// doc comment already establishes this exemption for a package the
// profile DOES mention at zero total statements ("nothing to measure, and
// Art.4 floors govern shipped behaviour, not empty packages"); this
// function extends the same test to a package the profile does not
// mention AT ALL, which is exactly the case DiscoverPackages must get
// right — treating every doc.go-only stub in this repo's later-wave
// package skeletons as "missing" would flag ~15 packages that have never
// shipped a line of logic, which is not what R-14.155 is about (a package
// whose TESTS existed and stopped compiling). A file counts as shippable
// if it declares anything beyond its own import block: any func, type,
// var, or const declaration.
func discoverDirHasShippableGoFile(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		shippable, err := fileHasNonImportDecl(fset, filepath.Join(dir, name))
		if err != nil {
			return false, err
		}
		if shippable {
			return true, nil
		}
	}
	return false, nil
}

// fileHasNonImportDecl parses path (imports only — no need for full type
// checking) and reports whether it declares anything beyond an import
// block: any FuncDecl, or any GenDecl whose token is not IMPORT (var,
// const, type). A file with none of those — just "package x" and
// comments, this repo's doc.go convention for a package nobody has
// started building yet — reports false.
func fileHasNonImportDecl(fset *token.FileSet, path string) (bool, error) {
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return false, fmt.Errorf("coverage gate: parsing %s: %w", path, err)
	}
	// A file excluded from the DEFAULT build by a //go:build constraint can
	// never appear in a default-tag coverage profile, so counting it here
	// would report a permanent, unfixable "missing" violation. This is
	// DERIVED from the file's own constraint, not a name list: the next
	// tag-gated package is handled without anyone remembering to add it.
	//
	// The distinction R-14.155 turns on is preserved exactly:
	// default-buildable but absent from the profile means "I could not
	// look" and stays a violation; constrained out of the default build
	// means "there was nothing to look at".
	if fileIsBuildConstrained(file) {
		return false, nil
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
// state this function does not re-adjudicate.
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

// fileIsBuildConstrained reports whether file carries a //go:build line.
// Any constraint at all excludes the file from the default build unless it
// is satisfied by default tags; this gate takes the conservative reading
// that a constrained file is not part of the default-lane profile, which is
// the only claim CheckCompleteness needs to make about it.
func fileIsBuildConstrained(file *ast.File) bool {
	for _, group := range file.Comments {
		// Constraints must precede the package clause.
		if group.Pos() > file.Package {
			break
		}
		for _, c := range group.List {
			if strings.HasPrefix(c.Text, "//go:build ") {
				return true
			}
		}
	}
	return false
}
