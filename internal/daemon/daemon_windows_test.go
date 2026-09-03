//go:build windows

package daemon

// Purpose: proves the Windows tier-2 refusal path actually RUNS on the
//   Windows CI lane (R-14.131: a platform that only compiles is not a
//   platform that has been proven) — every verb returns the same typed
//   KindUnsupported error whose hint names the daemonless fallback, and
//   Status carries its own distinct "not supported on this platform"
//   message, exactly as this ticket's contract requires.
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestWindows_Run_Refuses(t *testing.T) {
	err := Run(context.Background(), RunOptions{})
	assertWindowsRefusal(t, err, windowsRefusalHint)
}

func TestWindows_Start_Refuses(t *testing.T) {
	_, err := Start(context.Background(), StartOptions{})
	assertWindowsRefusal(t, err, windowsRefusalHint)
}

func TestWindows_Stop_Refuses(t *testing.T) {
	_, err := Stop(context.Background(), StopOptions{})
	assertWindowsRefusal(t, err, windowsRefusalHint)
}

func TestWindows_Restart_Refuses(t *testing.T) {
	_, err := Restart(context.Background(), RestartOptions{})
	assertWindowsRefusal(t, err, windowsRefusalHint)
}

// TestWindows_Status_HasItsOwnDistinctMessage proves status's refusal text
// is NOT the shared four-verb hint — the contract calls it out separately
// ("daemon status returns a typed 'daemon not supported on this
// platform' refusal").
func TestWindows_Status_HasItsOwnDistinctMessage(t *testing.T) {
	_, err := Status(context.Background(), StatusOptions{})
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("err = %v, want KindUnsupported", err)
	}
	if !strings.Contains(err.Error(), "daemon not supported on this platform") {
		t.Errorf("err = %q, want it to contain the platform-refusal message", err.Error())
	}
}

func assertWindowsRefusal(t *testing.T, err error, wantHintSubstr string) {
	t.Helper()
	if err == nil {
		t.Fatal("err = nil, want a typed Windows refusal")
	}
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Errorf("err = %v, want KindUnsupported", err)
	}
	if !strings.Contains(err.Error(), wantHintSubstr) {
		t.Errorf("err = %q, want it to contain %q", err.Error(), wantHintSubstr)
	}
}
