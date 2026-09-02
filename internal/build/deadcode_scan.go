// Package build (deadcode_scan.go) holds the file-collection and
// declared/used AST-scanning helpers ScanModuleSymbols (deadcode.go)
// delegates to. Split out under R-14.117 (Art.10.3's 300-line cap) —
// filecap_test.go's own gate is what caught deadcode.go going over and
// enforces the split stays honest.
package build

import (
	"go/ast"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// deadcodeCollectFiles walks each of roots under moduleRoot and returns
// every .go file found, testdata/ excluded.
func deadcodeCollectFiles(moduleRoot string, roots []string) ([]string, error) {
	var files []string
	for _, root := range roots {
		full := filepath.Join(moduleRoot, root)
		if _, statErr := os.Stat(full); statErr != nil {
			continue
		}
		walkErr := filepath.WalkDir(full, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".go") {
				files = append(files, path)
			}
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}
	return files, nil
}

// deadcodeCollectDeclared scans every non-test file in parsed for
// top-level exported symbol declarations.
func deadcodeCollectDeclared(moduleRoot string, fset *token.FileSet, parsed map[string]*ast.File) (map[DeadCodeSymbol]DeadCodeDecl, map[DeadCodeSymbol]token.Pos, error) {
	declared := make(map[DeadCodeSymbol]DeadCodeDecl)
	declPos := make(map[DeadCodeSymbol]token.Pos)

	for path, f := range parsed {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		dir, relErr := filepath.Rel(moduleRoot, filepath.Dir(path))
		if relErr != nil {
			return nil, nil, relErr
		}
		dir = filepath.ToSlash(dir)
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv != nil || !d.Name.IsExported() {
					continue
				}
				sym := DeadCodeSymbol{Dir: dir, Name: d.Name.Name}
				declared[sym] = DeadCodeDecl{File: path, Line: fset.Position(d.Pos()).Line, Doc: docText(d.Doc)}
				declPos[sym] = d.Name.Pos()
			case *ast.GenDecl:
				deadcodeCollectGenDecl(dir, path, fset, d, declared, declPos)
			}
		}
	}
	return declared, declPos, nil
}

// deadcodeCollectGenDecl handles the TypeSpec/ValueSpec half of
// deadcodeCollectDeclared (split out to keep both functions under the
// 50-line function cap, Art.10.3).
func deadcodeCollectGenDecl(dir, path string, fset *token.FileSet, d *ast.GenDecl, declared map[DeadCodeSymbol]DeadCodeDecl, declPos map[DeadCodeSymbol]token.Pos) {
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if !s.Name.IsExported() {
				continue
			}
			sym := DeadCodeSymbol{Dir: dir, Name: s.Name.Name}
			doc := docText(s.Doc)
			if doc == "" {
				doc = docText(d.Doc)
			}
			declared[sym] = DeadCodeDecl{File: path, Line: fset.Position(s.Pos()).Line, Doc: doc}
			declPos[sym] = s.Name.Pos()
		case *ast.ValueSpec:
			for _, n := range s.Names {
				if !n.IsExported() {
					continue
				}
				sym := DeadCodeSymbol{Dir: dir, Name: n.Name}
				doc := docText(s.Doc)
				if doc == "" {
					doc = docText(d.Doc)
				}
				declared[sym] = DeadCodeDecl{File: path, Line: fset.Position(n.Pos()).Line, Doc: doc}
				declPos[sym] = n.Pos()
			}
		}
	}
}

// deadcodeCollectUsed scans every file in parsed (shipped and test alike)
// for symbol usage: cross-package qualified selectors and same-package
// bare identifiers (excluding each symbol's own declaration position).
func deadcodeCollectUsed(moduleRoot, modulePath string, parsed map[string]*ast.File, declPos map[DeadCodeSymbol]token.Pos) (map[DeadCodeSymbol]bool, error) {
	used := make(map[DeadCodeSymbol]bool)
	for path, f := range parsed {
		dir, relErr := filepath.Rel(moduleRoot, filepath.Dir(path))
		if relErr != nil {
			return nil, relErr
		}
		dir = filepath.ToSlash(dir)
		aliasToDir := scanImportAliases(f, modulePath)
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok2 := sel.X.(*ast.Ident); ok2 {
					if d, tracked := aliasToDir[id.Name]; tracked {
						used[DeadCodeSymbol{Dir: d, Name: sel.Sel.Name}] = true
					}
				}
			}
			if id, ok := n.(*ast.Ident); ok {
				sym := DeadCodeSymbol{Dir: dir, Name: id.Name}
				if pos, tracked := declPos[sym]; tracked && pos != id.Pos() {
					used[sym] = true
				}
			}
			return true
		})
	}
	return used, nil
}

// scanImportAliases builds the local-identifier -> module-relative-dir map
// for a file's imports of the module's own packages.
func scanImportAliases(f *ast.File, modulePath string) map[string]string {
	out := make(map[string]string)
	prefix := modulePath + "/"
	for _, imp := range f.Imports {
		ip, err := strconv.Unquote(imp.Path.Value)
		if err != nil || !strings.HasPrefix(ip, prefix) {
			continue
		}
		relDir := strings.TrimPrefix(ip, prefix)
		name := relDir
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				continue
			}
			name = imp.Name.Name
		} else if idx := strings.LastIndex(relDir, "/"); idx >= 0 {
			name = relDir[idx+1:]
		}
		out[name] = relDir
	}
	return out
}

// docText renders a comment group as plain text, "" for nil.
func docText(cg *ast.CommentGroup) string {
	if cg == nil {
		return ""
	}
	return cg.Text()
}
