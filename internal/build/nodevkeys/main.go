// Command nodevkeys rejects release artifacts containing development custody.
package main

import (
	"fmt"
	"os"

	"github.com/acamarata/cascade/internal/build"
	"github.com/acamarata/cascade/internal/output"
)

// Purpose: enforce the artifact gate in release hooks. Inputs: process arguments.
// Outputs: diagnostic and failing exit status on rejection. Constraints: offline.
// SPORT: release artifact gate.
func main() {
	err := run(os.Args[1:])
	output.NewDefault(false, false, false, true).Fail(err)
	if err != nil {
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: nodevkeys <artifact>")
	}
	return build.CheckNoDevkeys(args[0])
}
