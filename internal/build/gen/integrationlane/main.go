// Command integrationlane plans, aggregates and asserts the integration CI
// lanes from the committed registry in internal/build.
//
//	integrationlane plan [--root DIR] [--os RUNNER] [--seeded]
//	integrationlane report --dir DIR --out FILE [--root DIR] [--os RUNNER]
//	integrationlane assert-red --report FILE
//
// plan checks the registry against the tree and ci.yml (exit 1 on any
// disagreement), then prints one "<package>\t<tags>\t<regex>" line per lane row
// that runs on RUNNER (default: this machine's runner); --seeded prints the
// seeded failing row of the red-proof job instead. report reads <DIR>/<n>.json
// (the `go test -json` output of row n, in plan order), aggregates it with
// goldencheck.Aggregate plus the package-identity and SkipOn rules, writes one
// JSON report and exits 1 unless every row passed. assert-red exits 0 only
// when a report shows the seeded row failing through TestSeededRed.
// Inputs: the module tree, ci.yml, captured go test -json files. Outputs:
// stdout lines and the report file. No subprocess: CI runs the rows.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/acamarata/cascade/internal/build"
	"github.com/acamarata/cascade/internal/output"
)

func main() {
	os.Exit(run(os.Args[1:], output.NewDefault(false, false, false, false)))
}

// options are the parsed flags of one invocation.
type options struct {
	root, runner, dir, out, reportFile string
	seeded                             bool
}

// run is main's whole body so tests can reach every exit path: 0 ok, 1 a
// failed plan, report or assertion, 2 usage.
func run(args []string, w *output.Writer) int {
	usage := errors.New("usage: integrationlane plan [--root DIR] [--os RUNNER] [--seeded] | report --dir DIR --out FILE [--root DIR] [--os RUNNER] | assert-red --report FILE")
	if len(args) == 0 {
		w.Fail(usage)
		return 2
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var o options
	fs.StringVar(&o.root, "root", ".", "module root")
	fs.StringVar(&o.runner, "os", defaultRunner(), "runner name (ubuntu-latest, macos-15, windows-latest)")
	fs.StringVar(&o.dir, "dir", "", "directory holding <n>.json go test -json output (report)")
	fs.StringVar(&o.out, "out", "", "report file to write (report)")
	fs.StringVar(&o.reportFile, "report", "", "report file to assert on (assert-red)")
	fs.BoolVar(&o.seeded, "seeded", false, "print the seeded red-proof row (plan)")
	if err := fs.Parse(args[1:]); err != nil {
		w.Fail(fmt.Errorf("integrationlane: %w", err))
		return 2
	}
	var err error
	switch args[0] {
	case "plan":
		err = planCmd(o, w)
	case "report":
		if o.dir == "" || o.out == "" {
			w.Fail(errors.New("integrationlane: report needs --dir and --out"))
			return 2
		}
		err = reportCmd(o, w)
	case "assert-red":
		if o.reportFile == "" {
			w.Fail(errors.New("integrationlane: assert-red needs --report"))
			return 2
		}
		err = assertRedCmd(o, w)
	default:
		w.Fail(usage)
		return 2
	}
	if err != nil {
		w.Fail(fmt.Errorf("integrationlane: %w", err))
		return 1
	}
	return 0
}

// defaultRunner maps this machine to the runner name its CI lane uses.
func defaultRunner() string {
	switch goruntime.GOOS {
	case "darwin":
		return build.RunnerMacOS
	case "windows":
		return build.RunnerWindows
	}
	return build.RunnerLinux
}

// laneRows returns the registry rows the integration-lanes job runs on runner.
func laneRows(runner string) []build.LaneRow {
	var rows []build.LaneRow
	for _, r := range build.IntegrationLanes {
		if r.Job == build.LaneJob && contains(r.OS, runner) {
			rows = append(rows, r)
		}
	}
	return rows
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// planCmd verifies the registry covers the tree, then prints the run plan.
func planCmd(o options, w *output.Writer) error {
	rows := []build.LaneRow{build.SeededRedRow}
	if !o.seeded {
		if err := checkRegistry(o.root); err != nil {
			return err
		}
		rows = laneRows(o.runner)
	}
	for _, r := range rows {
		w.Println(r.Package + "\t" + r.Tags + "\t" + r.RunRegex)
	}
	return nil
}

// checkRegistry runs the coverage gate against the tree under root.
func checkRegistry(root string) error {
	ci, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		return fmt.Errorf("%s is not a module root: %w", root, err)
	}
	pkgs, err := build.DiscoverIntegrationPackages(root)
	if err != nil {
		return err
	}
	if p := build.CheckLaneCoverage(pkgs, build.IntegrationLanes, build.IntegrationExcluded, ci); len(p) > 0 {
		return fmt.Errorf("the integration registry disagrees with the tree or ci.yml:\n  %s", strings.Join(p, "\n  "))
	}
	return nil
}
