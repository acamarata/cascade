// Purpose: windows stub for `cascade provider exec`.
// Inputs: unused.
// Outputs: KindUnsupported error.
// Constraints: refuses before exec, nothing started.
// SPORT: cli.provider.exec/ADD (P1-GWY-10a, contract:gateway-operator-exec).

//go:build windows

package main

import (
	"github.com/acamarata/cascade/pkg/cascade"
)

// platformExec refuses on windows with KindUnsupported.
func platformExec(path string, args []string, env []string) error {
	return cascade.New(cascade.KindUnsupported, "provider exec is not supported on windows")
}
