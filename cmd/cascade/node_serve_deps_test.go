// Purpose: unit coverage for productionNodeServeDeps, which shipped with
//
//	no direct caller in the existing `node serve` test suite (only the
//	injected-deps constructor is exercised elsewhere).
//
// SPORT: cmd.cascade.node-serve/TEST (node serve composition root).
package main

import (
	"runtime"
	"testing"
)

func TestProductionNodeServeDeps_AssemblesRealEnvironment(t *testing.T) {
	deps := productionNodeServeDeps()
	if deps.Paths == nil {
		t.Error("productionNodeServeDeps: Paths is nil")
	}
	if deps.Clock == nil {
		t.Error("productionNodeServeDeps: Clock is nil")
	}
	if deps.GOOS != runtime.GOOS {
		t.Errorf("productionNodeServeDeps: GOOS = %q, want %q", deps.GOOS, runtime.GOOS)
	}
}
