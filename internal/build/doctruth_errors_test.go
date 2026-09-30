package build

import (
	"errors"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// withSeam replaces one injection seam for the duration of the test and
// restores it via t.Cleanup, per the constraint that these tests never
// run in parallel (order independence under -shuffle=on is guaranteed by
// never overlapping two replacements of the same seam).
func withGitLsFiles(t *testing.T, fn func(string) ([]string, error)) {
	t.Helper()
	orig := gitLsFilesFn
	gitLsFilesFn = fn
	t.Cleanup(func() { gitLsFilesFn = orig })
}

func withReadFile(t *testing.T, fn func(string) ([]byte, error)) {
	t.Helper()
	orig := readFileFn
	readFileFn = fn
	t.Cleanup(func() { readFileFn = orig })
}

func withParseGoFile(t *testing.T, fn func(*token.FileSet, string, []byte) (*ast.File, error)) {
	t.Helper()
	orig := parseGoFileFn
	parseGoFileFn = fn
	t.Cleanup(func() { parseGoFileFn = orig })
}

// TestDocTruth_ErrorsFailClosed proves acceptance [1]: every listed
// failure mode returns a non-nil error and CheckDocTruth never returns an
// empty New slice dressed up as success. Each case lives in its own
// helper (funlen, Art.10.3's 50-line cap) run via t.Run.
func TestDocTruth_ErrorsFailClosed(t *testing.T) {
	base := docTruthIndexScaffold()
	base["README.md"] = "# Doc\n"

	t.Run("git ls-files failure", func(t *testing.T) { caseGitLsFilesFailure(t, base) })
	t.Run("unreadable doc", func(t *testing.T) { caseUnreadableDoc(t, base) })
	t.Run("unreadable link anchor target", caseUnreadableAnchorTarget)
	t.Run("missing baseline file", func(t *testing.T) { caseMissingBaselineFile(t, base) })
	t.Run("malformed baseline", func(t *testing.T) { caseMalformedBaseline(t, base) })
	t.Run("baseline entry with unknown rule", func(t *testing.T) { caseUnknownBaselineRule(t, base) })
	t.Run("zero files in scope", func(t *testing.T) { caseZeroFilesInScope(t, base) })
	t.Run("check argument is a missing path", func(t *testing.T) { caseMissingArgPath(t, base) })
	t.Run("check argument directory has no in-scope file, another arg valid", func(t *testing.T) { caseEmptyDirArg(t, base) })
	t.Run("go-parser failure on a non-test file names the file", caseParserFailureNonTest)
	t.Run("go-parser failure on a _test.go file names the file", caseParserFailureTestFile)
}

func caseGitLsFilesFailure(t *testing.T, base map[string]string) {
	root := newDocTruthRepo(t, base, nil)
	withGitLsFiles(t, func(string) ([]string, error) { return nil, errors.New("boom") })
	rep, err := CheckDocTruth(root, DocTruthCI)
	mustErr(t, err)
	if len(rep.New) != 0 || rep.Files != 0 {
		t.Fatalf("failure must not report a populated report: %+v", rep)
	}
}

func caseUnreadableDoc(t *testing.T, base map[string]string) {
	root := newDocTruthRepo(t, base, nil)
	withReadFile(t, func(p string) ([]byte, error) {
		if strings.HasSuffix(p, string(filepath.Separator)+"README.md") {
			return nil, &os.PathError{Op: "open", Path: p, Err: os.ErrPermission}
		}
		return os.ReadFile(p)
	})
	_, err := CheckDocTruth(root, DocTruthCI)
	mustErr(t, err)
}

func caseUnreadableAnchorTarget(t *testing.T) {
	files := docTruthIndexScaffold()
	files["README.md"] = "[x](docs/other.md#y)\n"
	files["docs/other.md"] = "# Other\n"
	root := newDocTruthRepo(t, files, nil)
	withReadFile(t, func(p string) ([]byte, error) {
		if strings.HasSuffix(p, filepath.FromSlash("/docs/other.md")) {
			return nil, &os.PathError{Op: "open", Path: p, Err: os.ErrPermission}
		}
		return os.ReadFile(p)
	})
	_, err := CheckDocTruth(root, DocTruthCI)
	mustErr(t, err)
}

func caseMissingBaselineFile(t *testing.T, base map[string]string) {
	root := t.TempDir()
	writeAllFiles(t, root, base)
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "user.email", "fixture@example.invalid")
	runGit(t, root, "config", "user.name", "Fixture")
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "chore: no baseline file at all")
	_, err := CheckDocTruth(root, DocTruthCI)
	mustErrKind(t, err, cascade.KindNotFound)
}

func caseMalformedBaseline(t *testing.T, base map[string]string) {
	root := newDocTruthRepo(t, base, nil)
	writeFileT(t, filepath.Join(root, baselineRelPath), "{ not json")
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "chore: corrupt baseline")
	_, err := CheckDocTruth(root, DocTruthCI)
	mustErr(t, err)
}

func caseUnknownBaselineRule(t *testing.T, base map[string]string) {
	root := newDocTruthRepo(t, base, []baselineEntry{{Key: "x|bogus|abc|1", File: "README.md", Rule: "bogus", Detail: "x"}})
	_, err := CheckDocTruth(root, DocTruthCI)
	mustErr(t, err)
}

func caseZeroFilesInScope(t *testing.T, base map[string]string) {
	root := newDocTruthRepo(t, base, nil)
	withGitLsFiles(t, func(string) ([]string, error) { return nil, nil })
	_, err := CheckDocTruth(root, DocTruthCI)
	mustErrKind(t, err, cascade.KindNotFound)
}

func caseMissingArgPath(t *testing.T, base map[string]string) {
	root := newDocTruthRepo(t, base, nil)
	_, err := CheckDocTruth(root, DocTruthCI, "docs/does-not-exist.md")
	mustErrKind(t, err, cascade.KindNotFound)
}

func caseEmptyDirArg(t *testing.T, base map[string]string) {
	root := newDocTruthRepo(t, base, nil)
	_, err := CheckDocTruth(root, DocTruthCI, "README.md", "docs/empty-dir-name")
	mustErrKind(t, err, cascade.KindNotFound)
}

func caseParserFailureNonTest(t *testing.T) {
	files := docTruthIndexScaffold()
	files["README.md"] = "Symbol: `internal/pkg1.RealFunc`\n"
	files["internal/pkg1/code.go"] = "package pkg1\n\nfunc RealFunc() {}\n"
	root := newDocTruthRepo(t, files, nil)
	withParseGoFile(t, func(fset *token.FileSet, filename string, src []byte) (*ast.File, error) {
		if strings.HasSuffix(filename, "code.go") {
			return nil, errors.New("syntax error")
		}
		return realParseGoFile(fset, filename, src)
	})
	_, err := CheckDocTruth(root, DocTruthCI)
	mustErrContains(t, err, "code.go")
}

func caseParserFailureTestFile(t *testing.T) {
	files := docTruthIndexScaffold()
	files["README.md"] = "Test: `TestSomething`\n"
	files["internal/pkg1/code_test.go"] = "package pkg1\n\nimport \"testing\"\n\nfunc TestSomething(t *testing.T) {}\n"
	root := newDocTruthRepo(t, files, nil)
	withParseGoFile(t, func(fset *token.FileSet, filename string, src []byte) (*ast.File, error) {
		if strings.HasSuffix(filename, "_test.go") {
			return nil, errors.New("syntax error")
		}
		return realParseGoFile(fset, filename, src)
	})
	_, err := CheckDocTruth(root, DocTruthCI)
	mustErrContains(t, err, "code_test.go")
}

// writeAllFiles writes each repo-relative path to content under root,
// creating parent directories, without committing (the caller commits).
func writeAllFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		target := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		writeFileT(t, target, content)
	}
}

func mustErr(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("want a non-nil error, got nil")
	}
}

func mustErrKind(t *testing.T, err error, want cascade.Kind) {
	t.Helper()
	mustErr(t, err)
	k, ok := cascade.KindOf(err)
	if !ok || k != want {
		t.Fatalf("err = %v, want Kind %v", err, want)
	}
}

func mustErrContains(t *testing.T, err error, sub string) {
	t.Helper()
	mustErr(t, err)
	if !strings.Contains(err.Error(), sub) {
		t.Fatalf("err = %v, want it to name %q", err, sub)
	}
}
