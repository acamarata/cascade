// Purpose (this file): the one subprocess this plugin ever forks — a
//
//	bounded, read-only `nself status --json` run INSIDE the directory
//	being asked about — plus the typed outcomes it reports.
//
// Inputs: the working directory (never optional: a probe about the wrong
//
//	directory is a wrong answer, not a slow one), the binary name, the
//	argument vector, and the deadline.
//
// Outputs: the child's stdout on success; otherwise one of
//
//	*binaryAbsentError, *probeTimeoutError, *probeFailedError, or a typed
//	KindUnavailable wrap for a child that could not be started. None of
//	them carries a byte of the child's own output: `nself status` prints
//	service state that can name a database URL, so that output never
//	enters this plugin's payload or its errors.
//
// Constraints: no ambient os.Environ() reaches the child — runnerEnv
//
//	builds a closed allowlist, mirroring internal/ci's AllowedEnv
//	precedent without importing it (Art.10.2). The child runs in its own
//	process group and the GROUP is signalled on the deadline
//	(exec_unix.go / exec_windows.go), so a grandchild cannot outlive the
//	probe, and cmd.WaitDelay bounds the pipe drain after the kill.
//
// SPORT: plugins/nself detect (ADD) — P1-E25-W5-S52-T2.

package nself

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// nselfBinary is the CLI this plugin probes.
const nselfBinary = "nself"

// probeArgs is the subprocess probe: `nself status --json`, the ONE
// project-scoped verb with a real JSON mode in the installed CLI (v1.3.5;
// `nself status --help` documents `-j, --json`). The contract's `nself
// project status --json` does not exist — see plugin.go's package doc and
// testdata/README.md.
var probeArgs = []string{"status", "--json"}

// probeTimeout is the DETECTION section's stated bound.
const probeTimeout = 2 * time.Second

// pipeDrainGrace bounds how long cmd.Wait waits for the probe's output
// pipes once its process group has been signalled — internal/ci's
// runner_exec.go precedent, for the same reason: without it a surviving
// pipe holder makes the timeout stop being a bound.
const pipeDrainGrace = 500 * time.Millisecond

// binaryAbsentError reports that the `nself` binary is not on PATH: not an
// nself project, and a distinct typed outcome from a marker miss so the
// doctor probe can tell the two apart. GOOS is carried because "absent" is
// the expected steady state on a platform nself does not ship for.
type binaryAbsentError struct {
	Binary string
	GOOS   string
}

func (e *binaryAbsentError) Error() string {
	return "nself: binary " + e.Binary + " not found on PATH (" + e.GOOS + ")"
}

// probeTimeoutError reports that the probe exceeded its bound.
type probeTimeoutError struct {
	Binary  string
	Timeout time.Duration
}

func (e *probeTimeoutError) Error() string {
	return "nself: probe of " + e.Binary + " exceeded " + e.Timeout.String()
}

// probeFailedError reports that the probe RAN and exited non-zero — the
// normal answer in a directory that is not an nself project (v1.3.5 exits
// 1 there). It carries the exit code and nothing else.
type probeFailedError struct {
	Binary   string
	ExitCode int
}

func (e *probeFailedError) Error() string {
	return "nself: " + e.Binary + " ran in the scanned directory and exited " + strconv.Itoa(e.ExitCode)
}

// subprocessRunner abstracts one bounded subprocess invocation.
type subprocessRunner interface {
	Run(ctx context.Context, dir, binary string, args []string, timeout time.Duration) ([]byte, error)
}

// execRunner is the real, production subprocessRunner. Its locator is
// injectable so a test proves the absent-binary path without reading this
// machine's PATH; the zero value is production behaviour.
type execRunner struct {
	locator binaryLocator
}

// envAllowlist is the closed set of variables the probe inherits when this
// process defines them. Unlike internal/ci's operator-extensible
// allowlist, nothing extends this one: nself needs to locate itself, its
// own config and a scratch directory, never an operator-declared extra.
var envAllowlist = []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL"}

// runnerEnv builds the child's whole environment from environ (injected,
// never read from os.Environ inside a code path a test exercises).
func runnerEnv(environ []string) []string {
	want := make(map[string]bool, len(envAllowlist))
	for _, k := range envAllowlist {
		want[k] = true
	}
	out := make([]string, 0, len(envAllowlist)+1)
	for _, kv := range environ {
		name, _, found := strings.Cut(kv, "=")
		if found && want[name] {
			out = append(out, kv)
		}
	}
	// Art.5.8 automation parity: the probe never prompts.
	return append(out, "CASCADE_NO_INPUT=1")
}

// Run implements subprocessRunner against a real child process.
func (r execRunner) Run(ctx context.Context, dir, binary string, args []string, timeout time.Duration) ([]byte, error) {
	resolved, err := r.lookup(binary)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, resolved, args...)
	cmd.Dir = dir
	cmd.Env = runnerEnv(os.Environ())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = pipeDrainGrace
	runErr := cmd.Run()
	return stdout.Bytes(), classifyRunErr(runCtx, binary, timeout, runErr)
}

// lookup resolves binary through this runner's locator (the real
// exec.LookPath in production).
func (r execRunner) lookup(binary string) (string, error) {
	loc := r.locator
	if loc == nil {
		loc = execLocator{}
	}
	return loc.Lookup(binary)
}

// classifyRunErr maps cmd.Run's outcome onto this package's typed
// outcomes, never carrying the child's own output.
func classifyRunErr(runCtx context.Context, binary string, timeout time.Duration, runErr error) error {
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return &probeTimeoutError{Binary: binary, Timeout: timeout}
	}
	if runErr == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return &probeFailedError{Binary: binary, ExitCode: exitErr.ExitCode()}
	}
	return cascade.Wrapf(cascade.KindUnavailable, runErr, "nself: starting %s", binary)
}
