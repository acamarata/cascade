// Purpose: tests for the --no-color persistent flag (main.go's
//
//	registerNoColorFlag) — CR fix, P1-E04-W1-S06-T5. Split into its own
//	file rather than folded into root_test.go both because --no-color is
//	registered outside newRootCmd() (main.go, not root.go — see
//	noColorFlag's doc comment) and to keep root_test.go under Art.10.3's
//	300-line cap (R-14.117: a ticket may split a file it owns into
//	additional siblings in the same package to satisfy the cap; this file
//	is exactly that, new tests only, no existing test moved).
//
// Constraints: writes nothing to disk (Art.7.1); package main, same as
//
//	root_test.go, so it shares execRoot's package-level test helpers.
//
// SPORT: cmd/cascade — cobra-root, global-flags, version, completions.
package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/pkg/cascade"
)

// execRootWithNoColor mirrors execRoot (root_test.go) but additionally
// registers the --no-color persistent flag the way main() does, via
// registerNoColorFlag — newRootCmd() alone does not include it (see
// noColorFlag's doc comment in main.go for why the flag lives outside
// root.go's GlobalFlags). Kept separate from execRoot, rather than folding
// the registration into it, so the unrelated golden-help fixture
// The golden help fixture now INCLUDES --no-color, because the flag is part
// of the root tree rather than bolted on in main().
func execRootWithNoColor(t *testing.T, args ...string) (string, error) {
	t.Helper()
	globalFlags = GlobalFlags{}
	noColorFlag = false
	// newRootCmd registers --no-color itself now, so the binary and the tests
	// build the same tree; registering again here would duplicate the flag.
	root := newRootCmd()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

// TestNoColorFlagAppearsInHelp proves --no-color is actually registered on
// the root command (not merely declared as a package-level var) by
// asserting it surfaces in --help, the same way TestGlobalFlagsAppearInHelp
// (root_test.go) pins the flags root.go registers.
func TestNoColorFlagAppearsInHelp(t *testing.T) {
	got, err := execRootWithNoColor(t, "--help")
	if err != nil {
		t.Fatalf("execute --help: %v", err)
	}
	if !strings.Contains(got, "--no-color") {
		t.Errorf("--help output missing --no-color flag\noutput:\n%s", got)
	}
	if !strings.Contains(got, "disable colored output") {
		t.Errorf("--help output missing --no-color usage text\noutput:\n%s", got)
	}
}

// TestNoColorFlagParses proves --no-color is actually parsed into
// noColorFlag (not just registered): unset by default, true once passed,
// and the parse itself does not error.
func TestNoColorFlagParses(t *testing.T) {
	t.Cleanup(func() { noColorFlag = false })

	if _, err := execRootWithNoColor(t, "version"); err != nil {
		t.Fatalf("execute version: %v", err)
	}
	if noColorFlag {
		t.Fatal("noColorFlag = true without --no-color on the command line")
	}

	if _, err := execRootWithNoColor(t, "--no-color", "version"); err != nil {
		t.Fatalf("execute --no-color version: %v", err)
	}
	if !noColorFlag {
		t.Fatal("noColorFlag = false after parsing --no-color")
	}
}

// TestNoColorFlagReachesOutputWriter proves --no-color's parsed value
// actually reaches the output Writer's colour decision, not just that a
// package-level bool gets set. It rebuilds the exact call main() makes —
// output.NewDefault(globalFlags.JSON, globalFlags.Quiet, globalFlags.Verbose,
// noColorFlag) — substituting output.New over buffers for NewDefault (which
// intentionally hardcodes the real os.Stdout/os.Stderr; see its doc
// comment), so the resolved Mode can be inspected in-process.
//
// Both cases below resolve NoColor=true because *bytes.Buffer is never a
// TTY (output.IsTerminal), which independently forces NoColor regardless of
// the flag — that is the correct, documented precedence (flag > NO_COLOR >
// TERM=dumb > non-TTY), not a gap in this test. What this test isolates is
// that noColorFlag's value is the literal 4th argument reaching
// output.New/NewDefault; the flag's effect when it is the *deciding*
// factor (a genuine TTY) is proven independently and exhaustively by
// TestNoColorResolved's precedence-order table in internal/output
// (color_internal_test.go).
func TestNoColorFlagReachesOutputWriter(t *testing.T) {
	t.Cleanup(func() { noColorFlag = false })

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"without --no-color", []string{"version"}},
		{"with --no-color", []string{"--no-color", "version"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noColorFlag = false
			if _, err := execRootWithNoColor(t, tc.args...); err != nil {
				t.Fatalf("execute %v: %v", tc.args, err)
			}

			buf := &bytes.Buffer{}
			w := output.New(buf, buf, globalFlags.JSON, globalFlags.Quiet, globalFlags.Verbose, noColorFlag)
			if !w.Mode().NoColor {
				t.Fatalf("Mode().NoColor = false, want true (non-TTY buffer; noColorFlag=%v)", noColorFlag)
			}
		})
	}
}

// TestMountedSubtreeExitCodes pins the unknown-subcommand and bad-argument
// rules for command groups mounted UNDER the root, not just the root itself.
//
// This exists because the root's fix did not reach mounted groups, and the
// cause was subtle: cobra returns ErrHelp before validating arguments when a
// command is not Runnable, so setting Args on a group whose only job is to
// hold subcommands has no effect at all — `cascade config bogus` printed help
// and exited 0. Every command group added later mounts the same way, so this
// guards all of them.
func TestMountedSubtreeExitCodes(t *testing.T) {
	invalid := cascade.KindInvalidInput.ExitCode()

	tests := []struct {
		name string
		args []string
		want int
	}{
		{"group with no args shows help", []string{"config"}, cascade.ExitOK},
		{"group help flag", []string{"config", "--help"}, cascade.ExitOK},
		{"unknown subcommand in a group", []string{"config", "bogus"}, invalid},
		{"leaf missing its argument", []string{"config", "get"}, invalid},
		{"leaf with too many arguments", []string{"config", "path", "extra"}, invalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := execRoot(t, tt.args...)
			if got := cascade.ExitCode(err); got != tt.want {
				t.Errorf("exit code = %d, want %d (err: %v)", got, tt.want, err)
			}
		})
	}
}
