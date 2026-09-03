//go:build windows

// Purpose: proves the Windows tier-2 refusal actually RUNS on the Windows
//
//	CI lane (R-14.131 — this repo's CI executes `go test ./...` natively
//	on windows-latest, not just cross-compiles it), matching the pattern
//	internal/daemon/daemon_windows_test.go already established for the
//	sibling lifecycle refusal.
//
// SPORT: internal/daemon/service (ADD, per T-2 sport_updates).
package service

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestWindowsRefusal proves both Install and Uninstall return the typed,
// non-nil KindUnsupported refusal with the daemonless-fallback hint —
// never a placeholder nil return (Art.1).
func TestWindowsRefusal(t *testing.T) {
	inst := NewInstaller()

	report, err := inst.Install(Config{})
	assertWindowsServiceRefusal(t, report, err, "install")

	report, err = inst.Uninstall(Config{})
	assertWindowsServiceRefusal(t, report, err, "uninstall")
}

func assertWindowsServiceRefusal(t *testing.T, report DeltaReport, err error, verb string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: want a typed Windows refusal, got nil", verb)
	}
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Errorf("%s: err kind = %v, want KindUnsupported", verb, err)
	}
	if !strings.Contains(err.Error(), verb) {
		t.Errorf("%s: err = %q, want it to name the verb", verb, err.Error())
	}
	if !strings.Contains(err.Error(), windowsServiceRefusalHint) {
		t.Errorf("%s: err = %q, want it to contain the hint", verb, err.Error())
	}
	if report != (DeltaReport{}) {
		t.Errorf("%s: report = %+v, want zero value", verb, report)
	}
}
