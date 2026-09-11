// Purpose: unit tests for runOutputWriter (run_exec.go) - the
//
//	non-interactive JSON forcing this ticket's §5.8 automation parity
//	requires, layered onto the existing --json flag. Isolated from
//	run_test.go (which documents its own no-net-import constraint) since
//	this file only needs cobra + output, no client/provider types.
//
// SPORT: cmd/cascade/run (ADD, P1-E11-W3-S23-T1/T3 sport_updates).
package main

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
)

// runOutputWriterTestCmd builds a bare *cobra.Command carrying the four
// local bool flags runOutputWriter reads, bound to an in-memory stdout
// buffer (never a terminal, per output.IsTerminal's documented contract).
func runOutputWriterTestCmd(stdout *bytes.Buffer) *cobra.Command {
	cmd := &cobra.Command{Use: "run"}
	cmd.SetOut(stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("quiet", false, "")
	cmd.Flags().Bool("verbose", false, "")
	cmd.Flags().Bool("no-color", false, "")
	return cmd
}

// TestRunOutputWriter_NonInteractiveForcesJSON pins the ticket's own
// requirement: CASCADE_NO_INPUT=1 forces JSON mode on even when --json
// was never passed.
func TestRunOutputWriter_NonInteractiveForcesJSON(t *testing.T) {
	cmd := runOutputWriterTestCmd(&bytes.Buffer{})
	deps := runDeps{Getenv: func(key string) string {
		if key == "CASCADE_NO_INPUT" {
			return "1"
		}
		return ""
	}}
	w := runOutputWriter(cmd, deps)
	if !w.Mode().JSON {
		t.Fatal("runOutputWriter under CASCADE_NO_INPUT=1 did not force JSON mode on")
	}
}

// TestRunOutputWriter_NonTTYStdoutForcesJSON pins the other non-interactive
// trigger: a non-terminal stdout (any *bytes.Buffer, matching a piped
// invocation) forces JSON on even with CASCADE_NO_INPUT unset.
func TestRunOutputWriter_NonTTYStdoutForcesJSON(t *testing.T) {
	cmd := runOutputWriterTestCmd(&bytes.Buffer{})
	deps := runDeps{Getenv: func(string) string { return "" }}
	w := runOutputWriter(cmd, deps)
	if !w.Mode().JSON {
		t.Fatal("runOutputWriter over a non-TTY stdout did not force JSON mode on")
	}
}

// TestRunOutputWriter_ExplicitJSONFlagHonored pins the pre-existing
// contract this ticket layers onto: --json=true still works on its own.
func TestRunOutputWriter_ExplicitJSONFlagHonored(t *testing.T) {
	cmd := runOutputWriterTestCmd(&bytes.Buffer{})
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("setting --json: %v", err)
	}
	deps := runDeps{Getenv: func(string) string { return "" }}
	w := runOutputWriter(cmd, deps)
	if !w.Mode().JSON {
		t.Fatal("runOutputWriter with --json=true did not report JSON mode on")
	}
}

// TestRunOutputWriter_QuietVerboseNoColorPassThrough pins that the other
// three flags flow into Mode unmodified - runOutputWriter must not
// silently drop them while wiring the new non-interactive check.
func TestRunOutputWriter_QuietVerboseNoColorPassThrough(t *testing.T) {
	cmd := runOutputWriterTestCmd(&bytes.Buffer{})
	for _, name := range []string{"quiet", "verbose", "no-color"} {
		if err := cmd.Flags().Set(name, "true"); err != nil {
			t.Fatalf("setting --%s: %v", name, err)
		}
	}
	deps := runDeps{Getenv: func(string) string { return "" }}
	w := runOutputWriter(cmd, deps)
	mode := w.Mode()
	if !mode.Quiet || !mode.Verbose || !mode.NoColor {
		t.Fatalf("runOutputWriter mode = %+v, want Quiet/Verbose/NoColor all true", mode)
	}
}
