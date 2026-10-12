// Command goldenlane plans and aggregates the golden-parity CI lane.
//
//	goldenlane plan [--root DIR]
//	goldenlane report --dir DIR --out FILE [--root DIR]
//
// plan prints one "<package>\t<regex>\t<root>" line per owner row and exits 1
// when a golden root is unmapped. report reads <DIR>/<n>.json (the `go test
// -json` output of row n in plan order), writes one JSON report to FILE,
// prints a text summary, and exits 1 unless every row passed.
// Inputs: the module tree and the committed owner table in goldencheck.
// Outputs: stdout lines, the report file. No subprocess: CI runs the rows.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/internal/goldencheck"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
)

func main() {
	os.Exit(run(os.Args[1:], output.NewDefault(false, false, false, false)))
}

// run is main's whole body so tests can reach every exit path: 0 ok, 1 a
// failed plan or report, 2 usage.
func run(args []string, w *output.Writer) int {
	if len(args) == 0 || (args[0] != "plan" && args[0] != "report") {
		w.Fail(errors.New("usage: goldenlane plan [--root DIR] | report --dir DIR --out FILE [--root DIR]"))
		return 2
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("root", ".", "module root")
	dir := fs.String("dir", "", "directory holding <n>.json go test -json output (report)")
	out := fs.String("out", "", "report file to write (report)")
	if err := fs.Parse(args[1:]); err != nil {
		w.Fail(fmt.Errorf("goldenlane: %w", err))
		return 2
	}
	rows, err := plan(*root)
	if err != nil {
		w.Fail(fmt.Errorf("goldenlane: %w", err))
		return 1
	}
	if args[0] == "plan" {
		for _, row := range rows {
			w.Println(row.Package + "\t" + row.RunRegex + "\t" + row.Root)
		}
		return 0
	}
	if *dir == "" || *out == "" {
		w.Fail(errors.New("goldenlane: report needs --dir and --out"))
		return 2
	}
	return report(rows, *dir, *out, w)
}

// plan discovers the roots under root and resolves them to rows.
func plan(root string) ([]goldencheck.Row, error) {
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return nil, fmt.Errorf("%s is not a module root: %w", root, err)
	}
	roots, err := goldencheck.DiscoverRoots(root)
	if err != nil {
		return nil, err
	}
	return goldencheck.Resolve(roots, goldencheck.Owners, goldencheck.Excluded)
}

// report aggregates <dir>/<n>.json for each row (a missing file leaves the
// row without events, which fails it), writes the JSON, prints the summary.
func report(rows []goldencheck.Row, dir, out string, w *output.Writer) int {
	events := make(map[string][]byte, len(rows))
	for i, row := range rows {
		if data, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(i+1)+".json")); err == nil {
			events[row.Root] = data
		}
	}
	rep := goldencheck.Aggregate(rows, events)
	data, err := json.MarshalIndent(rep, "", "  ")
	if err == nil {
		err = runtime.WriteFileAtomic(out, append(data, '\n'), 0o644)
	}
	if err != nil {
		w.Fail(fmt.Errorf("goldenlane: write report: %w", err))
		return 1
	}
	w.Println(strings.TrimSuffix(rep.Summary(), "\n"))
	if !rep.OK {
		return 1
	}
	return 0
}
