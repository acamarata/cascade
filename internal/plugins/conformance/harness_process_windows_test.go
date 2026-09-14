//go:build windows

// Package conformance (harness_process_windows_test.go): Purpose: the
// windows-tier stand-in for harness_process_test.go's processHarness.
// process.ProcessRuntime on windows (runtime_windows.go) is a distinct
// struct with no CapabilityChecker field at all, and
// process.HostCapabilityChecker itself does not exist on windows
// (hostcalls.go carries //go:build !windows) -- so this file cannot
// reference either. TestConformance_ProcessRuntime (suite_test.go)
// checks runtime.GOOS == "windows" itself and calls t.Skipf with the
// CI-asserted skip count before ever calling this constructor (Art.5:
// never a silent pass), so windowsProcessHarness.Call is unreachable in
// a normal run; it exists only so the package compiles on windows.
//
// SPORT: internal.plugins.conformance/ADDED (P1-E15-W4-S32-T2).
package conformance

import (
	"context"
	"testing"
)

type windowsProcessHarness struct{}

func newProcessHarness(context.Context, *testing.T) ABIHarness { return windowsProcessHarness{} }

func (windowsProcessHarness) Name() string         { return "process" }
func (windowsProcessHarness) CapabilityOnly() bool { return true }

func (windowsProcessHarness) Call(_ context.Context, t *testing.T, _ HostFnFixture) ABIObservation {
	t.Helper()
	t.Fatal("windowsProcessHarness.Call should be unreachable: TestConformance_ProcessRuntime skips before constructing a harness on windows")
	return ABIObservation{}
}
