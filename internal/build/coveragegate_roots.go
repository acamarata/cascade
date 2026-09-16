package build

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// Purpose (this file): which of the tree's packages are COMPOSITION ROOTS —
//
//	`package main` — so the coverage gate can apply R-14.239's role-based
//	tier instead of classifying by path prefix alone.
//
// Inputs: the module root.
// Outputs: the set of root-relative import paths whose package clause is
//
//	"main".
//
// Constraints: the package clause is read from the SOURCE, never inferred
//
//	from the directory name. "cmd/foo" is a convention; `package main` is
//	the fact, and a plugin binary lives under plugins/<name>/ where no
//	convention marks it. Parsed with parser.PackageClauseOnly, which stops
//	at the clause — the gate has no business reading further.
//
// SPORT: internal/build:coverage-roots (ADD) — P1-E01-W1-S01-T8 (R-14.239).

// MainPackages reports every root-relative import path under the scanned
// trees whose package clause is "main".
func MainPackages(root string) (map[string]bool, error) {
	paths, err := DiscoverPackages(root)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(paths))
	for _, importPath := range paths {
		isMain, err := dirDeclaresMain(filepath.Join(root, filepath.FromSlash(importPath)))
		if err != nil {
			return nil, err
		}
		if isMain {
			out[importPath] = true
		}
	}
	return out, nil
}

// dirDeclaresMain reports whether dir's buildable Go files declare
// `package main`.
//
// Test files are skipped: an external `_test` package sits beside the real
// one and would otherwise decide the answer for it.
func dirDeclaresMain(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.PackageClauseOnly)
		if err != nil {
			// A file this gate cannot parse is not evidence of anything;
			// the build and vet lanes own that failure.
			continue
		}
		return f.Name.Name == "main", nil
	}
	return false, nil
}
