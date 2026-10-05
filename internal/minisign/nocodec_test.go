// Purpose: keeps this package the only minisign codec. It walks the
//   non-test Go files under cmd/, internal/, pkg/, plugins/ and providers/
//   with go/parser and reports any file outside internal/minisign that
//   declares a func named like Parse*Minisign* or Verify*Minisign*, or that
//   carries the "untrusted comment:" header literal a minisign decoder needs.
// Inputs: the repository root (found from go.mod) or a fixture tree.
// Outputs: one violation string per offending file and declaration.
// SPORT: internal/minisign TestNoSecondMinisignCodec

package minisign

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var codecRoots = []string{"cmd", "internal", "pkg", "plugins", "providers"}

var codecFuncName = regexp.MustCompile(`(?i)^(parse|verify)\w*minisign`)

// headerLiteral is split so this file never carries the literal itself.
var headerLiteral = "untrusted" + " comment:"

// scanForSecondCodec returns the violations under root, skipping
// files directly in internal/minisign, test files and testdata directories.
func scanForSecondCodec(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	fset := token.NewFileSet()
	for _, top := range codecRoots {
		base := filepath.Join(root, top)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			// Only files directly in internal/minisign are the codec; a
			// subfolder there is scanned like any other package.
			if pathpkg.Dir(rel) == "internal/minisign" {
				return nil
			}
			found = append(found, violationsIn(t, fset, path, rel)...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", base, err)
		}
	}
	sort.Strings(found)
	return found
}

// violationsIn parses one file and reports codec-shaped declarations.
func violationsIn(t *testing.T, fset *token.FileSet, path, rel string) []string {
	t.Helper()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			if codecFuncName.MatchString(x.Name.Name) {
				out = append(out, rel+": func "+x.Name.Name+" looks like a minisign codec")
			}
		case *ast.BasicLit:
			if x.Kind != token.STRING {
				return true
			}
			if s, err := strconv.Unquote(x.Value); err == nil && strings.Contains(s, headerLiteral) {
				out = append(out, rel+": decodes the minisign header literal")
			}
		}
		return true
	})
	return out
}

// repoRootFromModule walks up from the test's directory to go.mod.
func repoRootFromModule(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

func TestNoSecondMinisignCodec(t *testing.T) {
	// The scan must see real files, or an empty tree would pass vacuously.
	root := repoRootFromModule(t)
	if _, err := os.Stat(filepath.Join(root, "internal", "nodes", "provision.go")); err != nil {
		t.Fatalf("scan root %s does not look like the repo: %v", root, err)
	}
	if got := scanForSecondCodec(t, root); len(got) != 0 {
		t.Fatalf("a second minisign codec exists outside internal/minisign:\n%s", strings.Join(got, "\n"))
	}
}

// TestNoSecondMinisignCodec_SeededViolationCaptured proves the scan is not
// vacuous: a copy of the parser in internal/nodes, a Verify*Minisign* name
// and a header-literal decoder are each reported, and clean files are not.
func TestNoSecondMinisignCodec_SeededViolationCaptured(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("internal/nodes/copy.go", "package nodes\n\nimport \"strings\"\n\nfunc ParseSignature(b []byte) bool {\n\treturn strings.HasPrefix(string(b), \""+headerLiteral+" \")\n}\n")
	write("pkg/x/named.go", "package x\n\nfunc VerifyMinisign() {}\n")
	write("cmd/clean/clean.go", "package clean\n\nfunc Verify() {}\n")
	write("internal/minisign/own.go", "package minisign\n\nfunc ParseMinisignOwn() { _ = \""+headerLiteral+"\" }\n")
	write("internal/minisign/legacy/minisign.go", "package legacy\n\nfunc ParseSignature(b []byte) bool {\n\treturn len(b) > 0 && string(b[:1]) == \""+headerLiteral+"\"\n}\n")
	write("internal/nodes/copy_test.go", "package nodes\n\nfunc ParseMinisignTest() {}\n")

	got := scanForSecondCodec(t, root)
	want := []string{
		"internal/minisign/legacy/minisign.go: decodes the minisign header literal",
		"internal/nodes/copy.go: decodes the minisign header literal",
		"pkg/x/named.go: func VerifyMinisign looks like a minisign codec",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("violations = %q, want %q", got, want)
	}
}
