// Purpose: the sweep's command-line surface — flag parsing, default
// resolution, and the process entry point. Never a shipped `cascade`
// subcommand; invoked only via `go run ./internal/migration/sweep` or the
// generate directive below (see the `go generate` line further down).
// SPORT: migration/sweep: term-file resolver rewrite.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/output"
)

//go:generate sh -c "go run . --files <(git ls-files -z)"

// Run is the sweep's testable core: parses args, runs the full
// scan/resolve/write cycle, and returns the exit code. main() and the
// generate directive above both invoke it. Codes: 0 success, 2
// missing/malformed input or unknown flag, 3 UNRESOLVED under
// --fail-on-unresolved. There is no --planning flag (moved to the term
// file's own replacement declarations); any unrecognized flag, including
// a legacy --planning, fails flag.Parse and returns 2.
func Run(args []string) int {
	return runArgs(args, output.NewDefault(false, false, false, false))
}

// runArgs is Run over an injected Writer, so tests can read the
// diagnostics (for example, which path an unreadable-file error names)
// without touching the real process streams.
func runArgs(args []string, w *output.Writer) int {
	fs := flag.NewFlagSet("sweep", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // usage and parse-error text go through w.Fail below, not flag's own stream
	filesFlag := fs.String("files", "", "NUL-separated tracked-file list (git ls-files -z output, - for stdin); required")
	dryRun := fs.Bool("dry-run", false, "print the table without writing docs or unresolved.json")
	rootFlag := fs.String("root", "", "the v2 tree to walk (default: repository root)")
	termsFlag := fs.String("terms", "", "term file path (default: <root>/internal/migration/sweep/testdata/v1-terms.json)")
	provFlag := fs.String("provenance", "", "provenance-refs file path (default: <root>/internal/migration/sweep/testdata/provenance-refs.json)")
	failOnUnresolved := fs.Bool("fail-on-unresolved", false, "exit 3 if any UNRESOLVED row exists")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			w.Fail(fmt.Errorf("sweep: %w\n%s", err, usageText(fs)))
			return 2
		}
		w.Fail(fmt.Errorf("sweep: %w", err))
		return 2
	}
	if *filesFlag == "" {
		w.Fail(fmt.Errorf("sweep: --files is required"))
		return 2
	}
	root := *rootFlag
	if root == "" {
		r, err := repoRoot()
		if err != nil {
			w.Fail(err)
			return 2
		}
		root = r
	}
	return runFromFlags(root, *termsFlag, *provFlag, *filesFlag, *dryRun, *failOnUnresolved, w)
}

// usageText renders fs's usage (the synopsis plus every flag's default
// and help) as a string, so -h reaches the user through the one
// internal/output.Writer rather than flag's own stream.
func usageText(fs *flag.FlagSet) string {
	var b strings.Builder
	b.WriteString("usage: go run ./internal/migration/sweep --files FILE [flags]\n")
	fs.SetOutput(&b)
	fs.PrintDefaults()
	fs.SetOutput(io.Discard)
	return strings.TrimRight(b.String(), "\n")
}

// runFromFlags loads the three fail-closed inputs (files list, term file,
// provenance refs) and dispatches to run(). Split out of Run() so flag
// parsing stays independently testable and Run() stays under the funlen cap.
func runFromFlags(root, termsFlag, provFlag, filesFlag string, dryRun, failOnUnresolved bool, w *output.Writer) int {
	termsPath := termsFlag
	if termsPath == "" {
		termsPath = filepath.Join(root, "internal", "migration", "sweep", "testdata", "v1-terms.json")
	}
	provPath := provFlag
	if provPath == "" {
		provPath = filepath.Join(root, "internal", "migration", "sweep", "testdata", "provenance-refs.json")
	}
	rawFiles, err := readFilesList(filesFlag)
	if err != nil {
		w.Fail(err)
		return 2
	}
	terms, err := LoadTermFile(termsPath)
	if err != nil {
		w.Fail(err)
		return 2
	}
	provRefs, err := LoadProvenanceRefs(provPath)
	if err != nil {
		w.Fail(err)
		return 2
	}
	files := filterTracked(rawFiles)
	return run(root, terms, provRefs, files, dryRun, failOnUnresolved, w)
}

func main() {
	os.Exit(Run(os.Args[1:]))
}
