// Purpose: process entrypoint for the single cascade binary.
// Inputs:   os.Args, os.Stdin/Stdout/Stderr (via cobra's default wiring).
// Outputs:  process exit code — 0 on success, 1 on any command error.
// Constraints: no business logic here (06-FORGE-SPEC §2, ONE binary rule);
//
//	this file only constructs the root command and executes it.
//
// SPORT: cmd/cascade — cobra-root, global-flags, version, completions.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
