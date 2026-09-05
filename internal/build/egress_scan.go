package build

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
)

// egressPatternMatches implements EgressDirAllowed's single-pattern rule.
func egressPatternMatches(dir, pattern string) bool {
	if strings.HasSuffix(pattern, "/*") {
		base := strings.TrimSuffix(pattern, "/*")
		return dir == base || strings.HasPrefix(dir, base+"/")
	}
	return dir == pattern
}

// egressSkippedDir excludes testdata/ (fixtures the toolchain never
// compiles) and dot dirs.
func egressSkippedDir(name string) bool {
	return name == "testdata" || (name != "." && strings.HasPrefix(name, "."))
}

// EgressImporter is one package directory that imports a governed stdlib
// path, together with the import that put it on the list.
type EgressImporter struct {
	// Dir is the module-relative package directory.
	Dir string
	// Import is the governed stdlib path it imports.
	Import string
}

// ScanEgressImporters walks root for non-test .go files and reports every
// package directory importing one of imports.
//
// Test files are excluded on purpose: a _test.go file importing net is
// governed by the no-network unit-lane gate, which is a different rule
// with a different remedy (the integration build tag). Folding the two
// together would make one exemption satisfy both.
func ScanEgressImporters(root string, imports []string) ([]EgressImporter, error) {
	governed := make(map[string]bool, len(imports))
	for _, path := range imports {
		governed[path] = true
	}
	seen := make(map[EgressImporter]bool)
	var out []EgressImporter
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if egressSkippedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		found, ferr := egressFileImports(path, governed)
		if ferr != nil {
			return ferr
		}
		dir := egressRelDir(root, path)
		for _, imp := range found {
			key := EgressImporter{Dir: dir, Import: imp}
			if !seen[key] {
				seen[key] = true
				out = append(out, key)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// egressFileImports returns the governed imports one file declares.
func egressFileImports(path string, governed map[string]bool) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, spec := range file.Imports {
		importPath, uerr := strconv.Unquote(spec.Path.Value)
		if uerr != nil {
			continue
		}
		if governed[importPath] {
			out = append(out, importPath)
		}
	}
	return out, nil
}

// egressRelDir returns the module-relative directory holding path.
func egressRelDir(root, path string) string {
	rel, err := filepath.Rel(root, filepath.Dir(path))
	if err != nil {
		return filepath.ToSlash(filepath.Dir(path))
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return ""
	}
	return rel
}
