// Purpose: the golden harvester's command-line surface. Never a shipped
// `cascade` subcommand; invoked only as `go run ./internal/migration/golden`
// or as a prebuilt binary run from the module tree.
// Inputs: [--domain memory|vault|accounts|config] [--dry-run].
// Outputs: exit 0 ok, 2 refused input, unknown flag or redaction failure;
// one summary line, or one error line, through internal/output.
// Constraints: the input root is always the module root's pinned dir; tests
// reach a scratch module only through the package-private moduleRootFor
// seam. The clock is injected (system clock in main only).
// SPORT: migration/golden/ADD (P1-E26-W10-S54-T3).
package main

import (
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"

	v1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// moduleRootFor locates the module root. A package var only so tests can
// point a run at a scratch module; Run never replaces it.
var moduleRootFor = findModuleRoot

// clockFor returns the run's clock. Tests replace it with a fixed clock.
var clockFor = func() runtime.Clock { return runtime.NewSystemClock() }

// Run is the harvester's testable core: it parses args, harvests and
// returns the exit code.
func Run(args []string) int {
	return runArgs(args, output.NewDefault(false, false, false, false))
}

// runArgs is Run over an injected Writer.
func runArgs(args []string, w *output.Writer) int {
	fs := flag.NewFlagSet("golden", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	domain := fs.String("domain", "", "harvest one domain: memory, vault, accounts or config (default: all)")
	dryRun := fs.Bool("dry-run", false, "compute and guard every fixture without writing")
	if err := fs.Parse(args); err != nil {
		w.Fail(cascade.Wrap(cascade.KindInvalidInput, err, "golden harvest: bad arguments"))
		return exitRefused
	}
	if fs.NArg() != 0 {
		w.Fail(cascade.New(cascade.KindInvalidInput, "golden harvest: unexpected positional arguments"))
		return exitRefused
	}
	domains, err := selectDomains(*domain)
	if err != nil {
		w.Fail(err)
		return exitRefused
	}
	root, err := moduleRootFor()
	if err != nil {
		w.Fail(err)
		return exitRefused
	}
	cfg := runConfig{moduleRoot: root, domains: domains, dryRun: *dryRun, clock: clockFor()}
	return harvestRun(context.Background(), cfg, w)
}

// selectDomains resolves the --domain flag.
func selectDomains(flagValue string) ([]v1.Domain, error) {
	if flagValue == "" {
		return inputDomains, nil
	}
	for _, domain := range inputDomains {
		if flagValue == string(domain) {
			return []v1.Domain{domain}, nil
		}
	}
	return nil, cascade.Newf(cascade.KindInvalidInput, "golden harvest: unknown domain %q", flagValue)
}

// findModuleRoot walks up from the working directory to the nearest go.mod.
func findModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "golden harvest: resolve the working directory")
	}
	for {
		if info, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil && info.Mode().IsRegular() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", cascade.New(cascade.KindNotFound, "golden harvest: no go.mod above the working directory")
		}
		dir = parent
	}
}

func main() {
	os.Exit(Run(os.Args[1:]))
}
