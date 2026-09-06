package build

// Purpose: asserts every tracked .go file in the real tree is gofmt-clean.
//   Nothing enforced this before R-14.201: the agent brief asked authors to
//   run `gofmt -l` on their own files, ci.yml never ran it, and no gate in
//   this package checked it, so two unformatted files reached main. An
//   assumed check is not a check.
// Inputs: the real tree, walked from the module root.
// Outputs: a failure naming every file whose gofmt output differs from its
//   contents.
// Constraints: reads only; never rewrites a file, because a gate that
//   silently fixes what it measures cannot fail. testdata/seeded-violations
//   is excluded by design: those files are deliberately malformed fixtures
//   for other gates' red-path tests.
// SPORT: internal.build.FmtGate/ADDED (T0, R-14.201).

import (
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGofmtClean_RealTreeGreen(t *testing.T) {
	root := coverageModuleRoot(t)
	var offenders []string

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".cover", "seeded-violations":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		formatted, fmtErr := format.Source(src)
		if fmtErr != nil {
			// A file that does not parse is some other gate's failure to
			// report, not this one's: say so rather than calling it
			// unformatted, which would send the reader to the wrong fix.
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, rel+" (does not parse: "+fmtErr.Error()+")")
			return nil
		}
		if string(formatted) != string(src) {
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("gofmt gate: walking the tree: %v", err)
	}
	if len(offenders) != 0 {
		t.Fatalf("gofmt gate: %d file(s) are not gofmt-clean; run `gofmt -w` on each:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}
