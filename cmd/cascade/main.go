// Purpose: process entrypoint for the single cascade binary.
// Inputs:   os.Args, os.Stdin/Stdout/Stderr (via cobra's default wiring for
//
//	command execution; internal/output.NewDefault for the error/exit path).
//
// Outputs:  process exit code — the error taxonomy's exit status for the
//
//	command result (0 on success), per R-14.113.
//
// Constraints: no business logic here (06-FORGE-SPEC §2, ONE binary rule);
//
//	this file only constructs the root command, adds the --no-color
//	persistent flag (D/S-06.T5 — added here rather than in root.go's
//	GlobalFlags, which belongs to D/S-06.T1's disjoint files_scope; see
//	the noColorFlag doc comment), executes it, and routes any resulting
//	error through internal/output — this file, and every file under
//	cmd/, must never write to os.Stdout/os.Stderr directly outside
//	internal/output (D/S-06.T5; enforced by the .golangci.yml forbidigo
//	rule this ticket adds).
//
// SPORT: cmd/cascade — cobra-root, global-flags, version, completions.
package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/output"
)

// noColorFlag backs the --no-color persistent flag (07-CLI-COMMAND-TREE
// global flags; D/S-06.T5 task 5). It is registered here, on the root
// command main.go itself constructs, rather than added as a field on
// root.go's GlobalFlags struct: this ticket's authorized write set is
// cmd/cascade/main.go only (files_scope), root.go belongs to D/S-06.T1,
// and a package-level flag target in the same package (main) works
// identically to one declared in GlobalFlags — cobra does not care which
// file registers a flag, only that some *bool target exists before
// Execute() parses it.
var noColorFlag bool

// registerNoColorFlag attaches the --no-color persistent flag to root,
// pointing it at the package-level noColorFlag var. Split out of main() as
// its own function (CR fix, P1-E04-W1-S06-T5) purely for testability: no
// test can call main() directly (it calls os.Exit), so nothing previously
// proved --no-color was registered, appeared in --help, parsed correctly,
// or that its value reached output.NewDefault's colour decision below.
// root_test.go now calls this same function against the same newRootCmd()
// tree main() builds, giving --no-color the same coverage every other
// persistent flag already has.
func registerNoColorFlag(root *cobra.Command) {
	root.PersistentFlags().BoolVar(&noColorFlag, "no-color", false,
		"disable colored output (also respects NO_COLOR; see docs/cli-output-contract.md)")
}

func main() {
	root := newRootCmd()

	err := root.Execute()

	// internal/output.NewDefault is the sole sanctioned place any file in
	// this module names os.Stdout/os.Stderr outside a _test.go file — see
	// that function's doc comment. globalFlags is populated by cobra
	// during Execute() above (root.go, same package), including on most
	// error paths (flag parsing fills in already-seen flags before
	// failing on the offending one), so it reflects the caller's actual
	// --json/-q/-v request even when the command itself failed.
	w := output.NewDefault(globalFlags.JSON, globalFlags.Quiet, globalFlags.Verbose, noColorFlag)
	if err != nil {
		w.Fail(err)
	}
	os.Exit(output.ExitCode(err))
}
