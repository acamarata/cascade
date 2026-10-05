package learn

// Purpose: restrict learned-table references to the four reviewed store files.
// Inputs: non-test Go source across the module, including constructed SQL.
// Outputs: violations and positive scan counts.
// Constraints: only export's exclusion declaration may name tables elsewhere.
// SPORT: learn/config_store_writer_test.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// learnedTableReferences counts table-name and table-constant mentions,
// including partial SQL statements, concatenations and comments.
func learnedTableReferences(src string) int {
	n := 0
	for _, marker := range []string{"jobs_learned_config", "tableLearnedConfig", "tableConfigSubmission", "tableConfigVersion"} {
		n += strings.Count(src, marker)
	}
	return n
}

// scanWriterReferences uses exact module-relative paths, never a prefix.
func scanWriterReferences(rel string, src []byte) (allowed, violations int, err error) {
	n := learnedTableReferences(string(src))
	if n == 0 {
		return 0, 0, nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		return 0, 0, err
	}
	if slices.Contains([]string{
		"internal/learn/config_store.go", "internal/learn/config_store_read.go",
		"internal/learn/config_store_versions.go", "internal/learn/migration_config.go",
	}, rel) {
		return n, 0, nil
	}
	if rel == "internal/storage/export.go" {
		src = maskExportExclusions(fset, f, src)
	}
	return 0, learnedTableReferences(string(src)), nil
}

// maskExportExclusions permits only the three table-name literals in the
// jobsDomainExcludedTables variable required by the export contract.
func maskExportExclusions(fset *token.FileSet, f *ast.File, src []byte) []byte {
	out := slices.Clone(src)
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			v, ok := spec.(*ast.ValueSpec)
			if !ok || len(v.Names) != 1 || v.Names[0].Name != "jobsDomainExcludedTables" || len(v.Values) != 1 {
				continue
			}
			list, ok := v.Values[0].(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, elem := range list.Elts {
				lit, ok := elem.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(lit.Value)
				if err == nil && slices.Contains([]string{"jobs_learned_config", "jobs_learned_config_submission", "jobs_learned_config_version"}, value) {
					for i := fset.Position(lit.Pos()).Offset; i < fset.Position(lit.End()).Offset; i++ {
						out[i] = ' '
					}
				}
			}
		}
	}
	return out
}

func TestSingleLearnedConfigWriter(t *testing.T) {
	isolateHome(t)
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil || fileMissing(filepath.Join(root, "go.mod")) {
		t.Fatalf("module root not found from the package dir: %v", err)
	}
	allowed, files := 0, 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && slices.Contains([]string{".git", ".claude", "testdata", "node_modules", "vendor"}, d.Name()) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files++
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		n, violations, err := scanWriterReferences(filepath.ToSlash(rel), src)
		allowed += n
		if violations != 0 {
			t.Errorf("learned-config table referenced outside reviewed files: %s (%d references)", rel, violations)
		}
		return err
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	if files < 100 || allowed < 6 {
		t.Fatalf("scanned %d files and saw %d allowed references; the scan is not reading the tree", files, allowed)
	}
}

func TestLearnedWriterScanConstructedSQL(t *testing.T) {
	isolateHome(t)
	for _, src := range []string{
		"const q = \"UPDATE \" + \"jobs_learned_config SET tier = 'safe'\"",
		"var q = fmt.Sprintf(\"UPDATE %s SET tier = 'safe'\", \"jobs_learned_config\")",
		"var q = tableLearnedConfig", "var q = tableConfigSubmission", "var q = tableConfigVersion",
		"// jobs_learned_config\nvar q = 1",
	} {
		for _, path := range []string{"internal/learn/config_store_future.go", "internal/other/config_store.go", "internal/storage/export.go"} {
			allowed, violations, err := scanWriterReferences(path, []byte("package x\n"+src))
			if err != nil || allowed != 0 || violations == 0 {
				t.Fatalf("missed %s in %s: %d/%d, %v", src, path, allowed, violations, err)
			}
		}
	}
	for _, name := range []string{"config_store.go", "config_store_read.go", "config_store_versions.go", "migration_config.go"} {
		allowed, violations, err := scanWriterReferences("internal/learn/"+name, []byte("package x\nconst q = \"jobs_learned_config\""))
		if err != nil || allowed != 1 || violations != 0 {
			t.Fatalf("reviewed %s refused: %d/%d, %v", name, allowed, violations, err)
		}
	}
}

func TestLearnedWriterScanExportException(t *testing.T) {
	isolateHome(t)
	const exclusion = "package x\nvar jobsDomainExcludedTables = []string{\"jobs_learned_config\", \"jobs_learned_config_submission\", \"jobs_learned_config_version\"}\n"
	for _, extra := range []string{"", "var q = \"UPDATE jobs_learned_config SET tier = 'safe'\"", "var q = tableConfigVersion"} {
		_, violations, err := scanWriterReferences("internal/storage/export.go", []byte(exclusion+extra))
		if err != nil || (violations == 0) != (extra == "") {
			t.Fatalf("export exception with %q: %d violations, %v", extra, violations, err)
		}
	}
	_, violations, err := scanWriterReferences("internal/other/export.go", []byte(exclusion))
	if err != nil || violations != 3 {
		t.Fatalf("exception leaked to another file: %d, %v", violations, err)
	}
}

// fileMissing reports whether path does not exist.
func fileMissing(path string) bool {
	_, err := os.Stat(path)
	return err != nil
}
