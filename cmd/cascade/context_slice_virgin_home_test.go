package main

// Purpose: DEFECT-context-slice-no-mkdir-virgin-home.md's regression test:
//   drive the REAL production command tree (newRootCmd with
//   productionContextScopeDeps, not a fake-Paths stub) against a CASCADE_HOME
//   that has never been created -- the exact virgin-HOME state the Wave-2
//   hardening gate hit on the shipped binary. The existing
//   TestContextSliceCLIBehavioralCoverage in context_cmd_test.go could not
//   have caught this: it seeds dir via t.TempDir(), which pre-creates the
//   directory, so resolveContextSliceEmbedded's sql.Open never saw a missing
//   parent. This test instead points CASCADE_HOME at a path under a fresh
//   t.TempDir() that is never itself created, so the data directory
//   genuinely does not exist until the command under test is expected to
//   create it.
// SPORT: cmd.cascade.cmd.context-slice-show (FIX, virgin-HOME bootstrap).

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// execRootProductionVirginHome drives the real command tree end to end:
// production deps throughout (no injected fakeContextScopePaths), a
// CASCADE_HOME pointed at a subdirectory that has not been created, and a
// CASCADE_SOCKET at a path with no listener, so the daemonless probe always
// takes the embedded path deterministically.
func execRootProductionVirginHome(t *testing.T, virginHome string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("CASCADE_HOME", virginHome)
	t.Setenv("CASCADE_SOCKET", filepath.Join(virginHome, "daemon.sock"))
	t.Setenv("HOME", t.TempDir())
	globalFlags = GlobalFlags{}

	root := newRootCmd()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

// TestContextSliceVirginHomeBootstraps is proof 1: the real CLI entry point,
// against a genuinely never-created CASCADE_HOME, must succeed -- not error
// with "unable to open database file (14)" the way the shipped binary did.
func TestContextSliceVirginHomeBootstraps(t *testing.T) {
	virginHome := filepath.Join(t.TempDir(), "never-created", ".cascade")
	if _, err := os.Stat(virginHome); !os.IsNotExist(err) {
		t.Fatalf("test setup bug: virginHome must not exist yet, stat err=%v", err)
	}

	got, err := execRootProductionVirginHome(t, virginHome, "context", "slice")
	if err != nil {
		t.Fatalf("cascade context slice on a virgin HOME: %v\noutput:\n%s", err, got)
	}
	if !strings.Contains(got, "SLOT") || !strings.Contains(got, "total") {
		t.Errorf("human output missing a slot line:\n%s", got)
	}

	dataDir := filepath.Join(virginHome, "data")
	if info, statErr := os.Stat(dataDir); statErr != nil || !info.IsDir() {
		t.Errorf("cascade context slice did not bootstrap %s: stat err=%v", dataDir, statErr)
	}
}

// TestContextScopeShowVirginHomeBootstraps is the sibling proof for
// `context scope show`, whose resolveContextScopeEmbedded shares the exact
// same dbPath-without-MkdirAll shape.
func TestContextScopeShowVirginHomeBootstraps(t *testing.T) {
	virginHome := filepath.Join(t.TempDir(), "never-created", ".cascade")
	if _, err := os.Stat(virginHome); !os.IsNotExist(err) {
		t.Fatalf("test setup bug: virginHome must not exist yet, stat err=%v", err)
	}

	got, err := execRootProductionVirginHome(t, virginHome, "context", "scope", "show")
	if err != nil {
		t.Fatalf("cascade context scope show on a virgin HOME: %v\noutput:\n%s", err, got)
	}
	if !strings.Contains(got, "kind") || !strings.Contains(got, "general") {
		t.Errorf("human output missing kind=general:\n%s", got)
	}

	dataDir := filepath.Join(virginHome, "data")
	if info, statErr := os.Stat(dataDir); statErr != nil || !info.IsDir() {
		t.Errorf("cascade context scope show did not bootstrap %s: stat err=%v", dataDir, statErr)
	}
}
