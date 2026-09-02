// Package build (deadcode_godoc.go) holds the Art.10.6
// godoc-on-exported-pkg-symbol gate and the runnable-Example presence
// check. Split out of deadcode.go under R-14.117 (Art.10.3's 300-line cap)
// — deadcode.go's own file-cap gate (filecap_test.go) is what enforces the
// split stays honest.
package build

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// GodocViolation is one exported pkg/ symbol with no godoc comment
// (Art.10.6).
type GodocViolation struct {
	File string
	Line int
	Name string
}

// ScanPkgGodoc walks pkgRoot (the module's pkg/ directory) and reports
// every exported top-level func/type/const/var declared in a non-test .go
// file with no preceding doc comment. testdata/ is skipped.
func ScanPkgGodoc(pkgRoot string) ([]GodocViolation, error) {
	var out []GodocViolation
	fset := token.NewFileSet()

	if _, err := os.Stat(pkgRoot); err != nil {
		return nil, nil
	}
	walkErr := filepath.WalkDir(pkgRoot, func(path string, d fs.DirEntry, err error) error {
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
		f, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			return perr
		}
		out = append(out, godocScanFileDecls(fset, path, f)...)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return out, nil
}

// godocScanFileDecls reports every exported, undocumented top-level
// func/type/const/var declared in f.
func godocScanFileDecls(fset *token.FileSet, path string, f *ast.File) []GodocViolation {
	var out []GodocViolation
	for _, decl := range f.Decls {
		switch d2 := decl.(type) {
		case *ast.FuncDecl:
			if d2.Name.IsExported() && d2.Doc == nil {
				out = append(out, GodocViolation{File: path, Line: fset.Position(d2.Pos()).Line, Name: d2.Name.Name})
			}
		case *ast.GenDecl:
			for _, spec := range d2.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name.IsExported() && s.Doc == nil && d2.Doc == nil {
						out = append(out, GodocViolation{File: path, Line: fset.Position(s.Pos()).Line, Name: s.Name.Name})
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n.IsExported() && s.Doc == nil && d2.Doc == nil {
							out = append(out, GodocViolation{File: path, Line: fset.Position(n.Pos()).Line, Name: n.Name})
						}
					}
				}
			}
		}
	}
	return out
}

// ExampleMissingViolation is one pkg/ subpackage that declares at least
// one exported top-level function but has zero runnable Example functions
// anywhere in its _test.go files (Art.10.6: "a runnable Example where the
// symbol is an entry point" — scoped per-package here: at least one
// Example demonstrating the package's surface, not one per exported
// symbol, which the article does not literally require and which would
// be a disproportionate retrofit of every existing package).
type ExampleMissingViolation struct {
	Dir string
}

// ScanPkgExamples walks pkgRoot's immediate subpackages (one level: pkg/X)
// and reports every one that declares an exported top-level function but
// has no func named "Example" or prefixed "Example" in any _test.go file.
func ScanPkgExamples(pkgRoot string) ([]ExampleMissingViolation, error) {
	entries, err := os.ReadDir(pkgRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []ExampleMissingViolation
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "testdata" {
			continue
		}
		dir := filepath.Join(pkgRoot, e.Name())
		hasExportedFunc, hasExample, scanErr := scanOnePkgForExamples(dir)
		if scanErr != nil {
			return nil, scanErr
		}
		if hasExportedFunc && !hasExample {
			out = append(out, ExampleMissingViolation{Dir: dir})
		}
	}
	return out, nil
}

// scanOnePkgForExamples inspects one directory's .go files (non-recursive
// — pkg/ subpackages in this repo are flat) for an exported top-level
// function (in a non-test file) and an Example* function (in a _test.go
// file).
func scanOnePkgForExamples(dir string) (hasExportedFunc, hasExample bool, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, false, err
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return false, false, perr
		}
		isTest := strings.HasSuffix(e.Name(), "_test.go")
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			switch {
			case isTest && strings.HasPrefix(fn.Name.Name, "Example"):
				hasExample = true
			case !isTest && fn.Name.IsExported():
				hasExportedFunc = true
			}
		}
	}
	return hasExportedFunc, hasExample, nil
}
