// Purpose (this file): TestScript — the executable half of this package's
//
//	testdata/scripts/*.txtar scenarios.
//
// WHY IT EXISTS. This module carries no rogpeppe/testscript dependency, so
//
//	every .txtar here has until now recorded a scenario that nothing ran.
//	That is a defensible way to write down intent, but it makes a ticket
//	check spelled `go test ./cmd/cascade/ -run TestScript` PASS BY NAMING
//	NOTHING: `go test -run <no match>` exits 0 (R-14.268). This driver runs
//	the scripts in process against the real root command, so the check
//	names something.
//
// OPT-IN, NOT RETROFIT. A script runs only if its header carries
//
//	`# testscript: executable`. The scenarios written before this driver
//	existed were never executed and are not assumed to pass; they stay
//	documentation until somebody converts one, and the runner NAMES every
//	file it skipped rather than quietly counting to zero.
//
// Constraints: in process, so there is no `go build` in the unit lane and
//
//	no network (Art.7). Each script gets its own CASCADE_HOME and HOME.
//
// SPORT: cmd/cascade testscript driver (ADD) — P1-E17-W4-S38-T3.
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/output"
)

// scriptMarker opts one .txtar into execution.
const scriptMarker = "# testscript: executable"

// scriptResult is one `exec` invocation's outcome.
type scriptResult struct {
	stdout, stderr string
	exit           int
}

// TestScript runs every executable script under testdata/scripts.
func TestScript(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "scripts", "*.txtar"))
	if err != nil {
		t.Fatalf("globbing scripts: %v", err)
	}
	var ran, skipped []string
	for _, file := range files {
		raw, err := os.ReadFile(file) //nolint:gosec // a path this test globbed itself
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		name := strings.TrimSuffix(filepath.Base(file), ".txtar")
		if !strings.Contains(string(raw), scriptMarker) {
			skipped = append(skipped, name)
			continue
		}
		ran = append(ran, name)
		t.Run(name, func(t *testing.T) { runScript(t, scriptBody(string(raw))) })
	}
	for _, name := range skipped {
		t.Logf("script %s is documentation only (no %q header); it was not run", name, scriptMarker)
	}
	if len(ran) == 0 {
		t.Fatalf("no executable script found among %d files; a check that names this test would "+
			"pass having run nothing", len(files))
	}
}

// scriptBody returns the script half of a txtar archive: everything before
// the first file header.
func scriptBody(raw string) string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "-- ") && strings.HasSuffix(strings.TrimSpace(line), " --") {
			break
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// runScript executes one script's directives in order.
//
// An UNKNOWN directive is a failure rather than a skip. A runner that
// ignored what it did not understand would let a script assert nothing
// while still reporting a pass, which is the failure this file exists to
// stop.
func runScript(t *testing.T, script string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CASCADE_HOME", filepath.Join(home, ".cascade"))

	var last scriptResult
	for i, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negated := false
		if rest, ok := strings.CutPrefix(line, "! "); ok {
			negated, line = true, strings.TrimSpace(rest)
		}
		verb, rest, _ := strings.Cut(line, " ")
		rest = strings.TrimSpace(rest)
		switch verb {
		case "env":
			scriptEnv(t, i+1, rest)
		case "exec":
			last = scriptExec(t, i+1, rest, negated)
		case "stdout":
			scriptMatch(t, i+1, "stdout", last.stdout, rest, negated)
		case "stderr":
			scriptMatch(t, i+1, "stderr", last.stderr, rest, negated)
		default:
			t.Fatalf("line %d: %q is not a directive this runner implements "+
				"(env, exec, stdout, stderr); a silently-ignored line asserts nothing", i+1, verb)
		}
	}
}

// scriptEnv applies one `env KEY=VALUE` line.
func scriptEnv(t *testing.T, line int, rest string) {
	t.Helper()
	key, value, ok := strings.Cut(rest, "=")
	if !ok {
		t.Fatalf("line %d: env needs KEY=VALUE, got %q", line, rest)
	}
	t.Setenv(key, value)
}

// scriptExec runs one `exec cascade ...` line against the real root
// command, in process, and checks its exit status against the directive's
// own expectation.
func scriptExec(t *testing.T, line int, rest string, wantFailure bool) scriptResult {
	t.Helper()
	fields := strings.Fields(rest)
	if len(fields) == 0 || fields[0] != "cascade" {
		t.Fatalf("line %d: exec runs the cascade binary only, got %q", line, rest)
	}
	got := execCascade(fields[1:])
	switch {
	case wantFailure && got.exit == 0:
		t.Fatalf("line %d: `%s` succeeded; the script expects it to fail\nstdout: %s\nstderr: %s",
			line, rest, got.stdout, got.stderr)
	case !wantFailure && got.exit != 0:
		t.Fatalf("line %d: `%s` exited %d\nstdout: %s\nstderr: %s",
			line, rest, got.exit, got.stdout, got.stderr)
	}
	return got
}

// execCascade runs the root command with args, returning what a process
// running the same argv would have written and exited with.
//
// It reproduces main()'s own error path — output.ExitCode plus Writer.Fail
// — rather than inspecting the error, so a script asserts the exit status
// and the diagnostic an operator would actually get.
func execCascade(args []string) scriptResult {
	globalFlags = GlobalFlags{}
	noColorFlag = false
	var out, errBuf bytes.Buffer
	// newRootCmd registers --no-color itself, so the binary and the tests
	// build the same tree; registering it again here would panic.
	root := newRootCmd()
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SilenceErrors = true
	root.SilenceUsage = true
	err := root.Execute()
	if err != nil {
		output.New(&out, &errBuf, globalFlags.JSON, globalFlags.Quiet, globalFlags.Verbose, true).Fail(err)
	}
	return scriptResult{stdout: out.String(), stderr: errBuf.String(), exit: output.ExitCode(err)}
}

// unquoteScriptArg strips the quotes testscript's own grammar puts round
// a pattern, so a script reads the way one written for the real runner
// would. A pattern left quoted would compile — as a regexp matching
// literal quote characters — and then never match anything, which is the
// quietest possible way for an assertion to stop asserting.
func unquoteScriptArg(s string) string {
	for _, q := range []string{"'", `"`} {
		if len(s) >= 2 && strings.HasPrefix(s, q) && strings.HasSuffix(s, q) {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// scriptMatch applies one stdout/stderr regexp assertion.
func scriptMatch(t *testing.T, line int, stream, got, pattern string, negated bool) {
	t.Helper()
	pattern = unquoteScriptArg(pattern)
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("line %d: %s pattern %q does not compile: %v", line, stream, pattern, err)
	}
	switch matched := re.MatchString(got); {
	case negated && matched:
		t.Errorf("line %d: %s matched %q and the script says it must not:\n%s", line, stream, pattern, got)
	case !negated && !matched:
		t.Errorf("line %d: %s did not match %q:\n%s", line, stream, pattern, got)
	}
}
