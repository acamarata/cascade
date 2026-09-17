//go:build !windows

package main

// Purpose (this file): the one assertion that `cascade init` advertises
//   the database this installation actually uses.
// SPORT: cmd/cascade test-support (ADD, P1-E16-W4-S35-T15).

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/runtime"
)

// TestInitAdvertisesTheDatabaseTheProductOpens is a CROSS-SUBSYSTEM
// assertion, and it has to be: the defect was that two subsystems derived
// the layout separately and got different answers.
//
// `cascade init` used to compute the local database as
// <root>/cascade.db while the daemon, the embedded verbs and every doctor
// check open <root>/data/cascade.db. So the wizard printed a path nothing
// opened, and its storage probe left an empty database sitting there for
// an operator to copy, inspect or delete instead of the real one
// (R-14.279).
//
// The proof is not "init's path equals the expression init uses" — that
// restates the bug's own logic and would have passed throughout. It is:
// open the store through the REAL production path (openPluginStore, which
// funnels into openRuntimeStore, the daemon's own composition root), then
// require a file to exist where init says the database is.
func TestInitAdvertisesTheDatabaseTheProductOpens(t *testing.T) {
	paths := fakeDaemonPaths{root: t.TempDir()}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}

	store, closeStore, err := openPluginStore(context.Background(), paths, runtime.NewSystemClock())
	if err != nil {
		t.Fatalf("opening the embedded store the product really uses: %v", err)
	}
	if store == nil {
		t.Fatal("openPluginStore returned a nil store with no error")
	}
	closeStore()

	deps, err := productionInitDeps(initCmdForPathTest(t), paths)
	if err != nil {
		t.Fatalf("productionInitDeps: %v", err)
	}
	if deps.LocalDBPath == "" {
		t.Fatal("init was wired with no database path; the wizard would refuse rather than report one")
	}
	if _, statErr := os.Stat(deps.LocalDBPath); statErr != nil {
		t.Fatalf("init advertises %q, and nothing in the product put a database there: %v\n"+
			"the real one is at %q", deps.LocalDBPath, statErr, filepath.Join(paths.DataDir(), "cascade.db"))
	}
}

// initCmdForPathTest is the minimum cobra command productionInitDeps
// reads its two flags off.
func initCmdForPathTest(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "init"}
	cmd.Flags().Bool("yes", true, "")
	cmd.Flags().Bool("check", false, "")
	return cmd
}
