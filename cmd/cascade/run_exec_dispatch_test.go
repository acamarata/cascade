// Purpose: unit coverage for fetchRun and runRunCmd (run_exec.go) -- the
//
//	two dispatch-half functions that shipped with no direct test, because
//	their happy paths dial the daemon and this package's default unit
//	lane may not import net (hygiene.go's no-network gate). Their REFUSAL
//	paths need no socket at all: both fail before the dial, on a
//	malformed config.toml or an invalid flag, and those are the paths a
//	user actually hits when the daemon is not configured.
//
// Mirrors run_exec_paths_test.go's fakeDaemonPaths + malformed-config
// setup exactly, for the two callers of the function it already covers.
//
// SPORT: cmd/cascade/run/TEST (coverage ratchet, cmd/cascade).
package main

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// runDispatchDeps builds runDeps over a temp data dir whose config.toml is
// deliberately unparseable, so every path under test refuses at
// resolveRunSocket and never reaches a dial.
func runDispatchDeps(t *testing.T) runDeps {
	t.Helper()
	deps := runDeps{
		Paths:   fakeDaemonPaths{root: t.TempDir()},
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
	}
	if err := os.WriteFile(deps.Paths.ConfigPath(), []byte("this is not [ valid toml"), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	return deps
}

// TestFetchRun_PropagatesSocketResolutionFailure proves fetchRun returns
// resolveRunSocket's typed refusal rather than dialing anyway with a
// zero-value Settings (which would dial the empty path and report a
// confusing connection error instead of the real cause).
func TestFetchRun_PropagatesSocketResolutionFailure(t *testing.T) {
	resp, err := fetchRun(context.Background(), runDispatchDeps(t), runRequestParams{TaskClass: "chat"})
	if err == nil {
		t.Fatal("fetchRun: expected a refusal against a malformed config.toml")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
	if resp.JobID != "" || resp.Output != "" {
		t.Errorf("fetchRun returned a populated response alongside its error: %+v", resp)
	}
}

// TestRunRunCmd_RefusesInvalidFlagsBeforeDispatch proves runRunCmd returns
// buildRunParams' refusal without building a writer or touching the
// daemon: an invalid --sensitivity tier must never reach the wire.
func TestRunRunCmd_RefusesInvalidFlagsBeforeDispatch(t *testing.T) {
	cmd := runOutputWriterTestCmd(&bytes.Buffer{})
	cmd.SetIn(bytes.NewReader(nil))
	err := runRunCmd(cmd, runDispatchDeps(t), runFlags{Task: "chat", Sensitivity: "not-a-tier"})
	if err == nil {
		t.Fatal("runRunCmd: expected a refusal for an invalid --sensitivity tier")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
}

// TestRunRunCmd_PropagatesDispatchFailure proves the other half: valid
// flags, so buildRunParams succeeds and runRunCmd goes on to build its
// writer and dispatch, and fetchRun's refusal is returned unchanged
// rather than being swallowed into a rendered empty result.
func TestRunRunCmd_PropagatesDispatchFailure(t *testing.T) {
	stdout := &bytes.Buffer{}
	cmd := runOutputWriterTestCmd(stdout)
	cmd.SetIn(bytes.NewReader(nil))
	err := runRunCmd(cmd, runDispatchDeps(t), runFlags{Task: "chat"})
	if err == nil {
		t.Fatal("runRunCmd: expected the dispatch refusal to propagate")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("runRunCmd wrote a result to stdout despite failing: %q", stdout.String())
	}
}
