// Purpose: DEFECT-cli-surfaces-promise-embedded-mode.md's regression
// test: probeDaemonlessAndAttach must never print its "embedded
// (daemonless) mode" warning for `cascade run`, since run_exec.go's
// fetchRun has no embedded fallback at all and always refuses when
// daemonless — printing that warning first would still be the DEFECT's
// exact shape (promise embedded mode, then hand the user a refusal).
// Every OTHER command keeps the warning, so this file also proves the
// suppression is scoped to `cascade run` alone, mirroring
// root_daemon_run_test.go's identical structure for `cascade daemon run`.
//
// Build constraint: reuses captureRealStderr and shortCascadeHome
// (root_daemon_run_test.go, testhelper_home_test.go). captureRealStderr
// lives in a !windows-tagged file, so this file carries the same tag
// (the exact build-tag-parity trap the phase brief warns about).
//go:build !windows

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestProbeDaemonlessAndAttach_RunCmdNoSpuriousWarning is the
// mutation-proof target: removing isRunCmd from probeDaemonlessAndAttach's
// condition (root.go) makes this fail with the warning present in the
// captured output.
func TestProbeDaemonlessAndAttach_RunCmdNoSpuriousWarning(t *testing.T) {
	home := shortCascadeHome(t)
	t.Setenv("CASCADE_HOME", home)
	t.Setenv("HOME", home)
	globalFlags = GlobalFlags{}

	root := &cobra.Command{Use: "cascade"}
	runCmd := &cobra.Command{Use: "run"}
	root.AddCommand(runCmd)

	out := captureRealStderr(t, func() {
		probeDaemonlessAndAttach(context.Background(), runCmd)
	})
	if strings.Contains(out, "embedded (daemonless) mode") {
		t.Fatalf("cascade run printed the embedded-mode warning about itself: %q", out)
	}
}

// TestProbeDaemonlessAndAttach_StatusCmdNoSpuriousWarning covers the same
// shape for `cascade status`, which likewise refuses outright when
// daemonless (version, pid, uptime and connection count are live snapshots
// of the daemon process, with no honest embedded answer). Removing
// isStatusCmd from probeDaemonlessAndAttach's condition makes this fail
// with the warning present.
func TestProbeDaemonlessAndAttach_StatusCmdNoSpuriousWarning(t *testing.T) {
	home := shortCascadeHome(t)
	t.Setenv("CASCADE_HOME", home)
	t.Setenv("HOME", home)
	globalFlags = GlobalFlags{}

	root := &cobra.Command{Use: "cascade"}
	statusCmd := &cobra.Command{Use: "status"}
	root.AddCommand(statusCmd)

	out := captureRealStderr(t, func() {
		probeDaemonlessAndAttach(context.Background(), statusCmd)
	})
	if strings.Contains(out, "embedded (daemonless) mode") {
		t.Fatalf("cascade status printed the embedded-mode warning before refusing: %q", out)
	}
}

// TestProbeDaemonlessAndAttach_OtherCmdStillWarns proves the suppression is
// scoped to the commands that genuinely have no embedded path. `recall`
// DOES have one (recall_embedded.go), so it must keep the warning: probed
// the identical way against the same daemonless CASCADE_HOME, it still
// gets it exactly as before this fix.
func TestProbeDaemonlessAndAttach_OtherCmdStillWarns(t *testing.T) {
	home := shortCascadeHome(t)
	t.Setenv("CASCADE_HOME", home)
	t.Setenv("HOME", home)
	globalFlags = GlobalFlags{}

	root := &cobra.Command{Use: "cascade"}
	other := &cobra.Command{Use: "recall"}
	root.AddCommand(other)

	out := captureRealStderr(t, func() {
		probeDaemonlessAndAttach(context.Background(), other)
	})
	if !strings.Contains(out, "embedded (daemonless) mode") {
		t.Fatalf("cascade recall lost the daemonless warning: %q", out)
	}
}
