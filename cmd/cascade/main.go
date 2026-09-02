// Purpose: process entrypoint for the single cascade binary.
// Inputs:   os.Args, os.Stdin/Stdout/Stderr (via cobra's default wiring).
// Outputs:  process exit code — the error taxonomy's exit status for the
//
//	command result (0 on success), per R-14.113.
//
// Constraints: no business logic here (06-FORGE-SPEC §2, ONE binary rule);
//
//	this file only constructs the root command and executes it.
//
// SPORT: cmd/cascade — cobra-root, global-flags, version, completions.
package main

import (
	"fmt"
	"os"

	"github.com/acamarata/cascade/pkg/cascade"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(cascade.ExitCode(err))
	}
}
