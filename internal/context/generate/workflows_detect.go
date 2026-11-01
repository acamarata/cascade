package generate

// Purpose: ExecProbe, the WorkflowsSupportProbe that asks the installed
//   harness CLI what it offers (contract:workflows-probe).
// Inputs: an injected LookPath and Run; PATH from the environment.
// Outputs: a ProbeResult from exactly two invocations, `--version` and
//   `--help`.
// Constraints: no other argument is ever passed and no subcommand is ever
//   invoked. The child sees only PATH and HOME, HOME is a fresh empty
//   temp directory removed afterwards, its working directory is another
//   empty temp directory, stdin is the null device and each invocation
//   has five seconds. No credential or CASCADE_* variable reaches it.
// SPORT: context-engine/workflows-probe (ADD, P1-GEN-09).

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// harnessCLI is the program name ExecProbe resolves through LookPath.
const harnessCLI = "claude"

// probeTimeout bounds each of the two invocations.
const probeTimeout = 5 * time.Second

// probeOutputCap bounds the bytes kept from one invocation's stdout.
const probeOutputCap = 1 << 20

// probeArgs are the only two argument lists ExecProbe ever passes.
var probeArgs = [][]string{{"--version"}, {"--help"}}

// ExecProbe probes the harness CLI through injected LookPath and Run.
type ExecProbe struct {
	// LookPath resolves the harness program name to a binary path.
	LookPath func(string) (string, error)
	// Run runs bin with args and exactly env, returning its stdout and
	// exit status; err is set only when the program could not be run.
	Run func(ctx context.Context, bin string, args []string, env []string) (stdout []byte, exit int, err error)
}

// NewExecProbe returns the production probe: exec.LookPath and a runner
// built on exec.CommandContext.
func NewExecProbe() *ExecProbe {
	return &ExecProbe{LookPath: exec.LookPath, Run: runIsolated}
}

// Probe runs `--version` then `--help` and classifies the output. A
// binary that LookPath cannot find is unsupported; an invocation that
// cannot run or times out is indeterminate. A canceled ctx is
// KindCanceled.
func (p *ExecProbe) Probe(ctx context.Context) (ProbeResult, error) {
	if p == nil || p.LookPath == nil || p.Run == nil {
		return ProbeResult{}, cascade.New(cascade.KindInvalidInput, "generate: workflows probe needs LookPath and Run")
	}
	bin, err := p.LookPath(harnessCLI)
	if err != nil || bin == "" {
		return ProbeResult{Result: resultUnsupported, ExitCode: -1}, nil
	}
	home, err := os.MkdirTemp("", "cascade-probe-home-")
	if err != nil {
		return ProbeResult{}, cascade.Wrap(cascade.KindUnavailable, err, "generate: workflows probe home")
	}
	defer func() { _ = os.RemoveAll(home) }()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
	var outs [2][]byte
	var exits [2]int
	for i, args := range probeArgs {
		out, exit, err := p.invoke(ctx, bin, args, env)
		if cerr := ctx.Err(); cerr != nil {
			return ProbeResult{}, cascade.Wrap(cascade.KindCanceled, cerr, "generate: workflows probe canceled")
		}
		if err != nil {
			exit = -1
		}
		outs[i], exits[i] = out, exit
	}
	result, version := classify(outs[0], exits[0], outs[1], exits[1])
	exit := exits[1]
	if exits[0] != 0 {
		exit = exits[0]
	}
	return ProbeResult{Result: result, Binary: bin, Version: version, ExitCode: exit}, nil
}

// invoke runs one invocation under its own five-second deadline, with a
// fresh copy of args and env so Run cannot alter the next call's.
func (p *ExecProbe) invoke(ctx context.Context, bin string, args, env []string) ([]byte, int, error) {
	ictx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, exit, err := p.Run(ictx, bin, append([]string(nil), args...), append([]string(nil), env...))
	if err == nil && ictx.Err() != nil {
		err = ictx.Err()
	}
	return out, exit, err
}

// runIsolated runs bin in a fresh empty working directory with exactly
// env, stdin on the null device and stdout capped. A non-zero exit is an
// exit status, not an error.
func runIsolated(ctx context.Context, bin string, args []string, env []string) ([]byte, int, error) {
	cwd, err := os.MkdirTemp("", "cascade-probe-cwd-")
	if err != nil {
		return nil, -1, err
	}
	defer func() { _ = os.RemoveAll(cwd) }()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env, cmd.Dir, cmd.Stdin = env, cwd, nil
	cmd.Stdout, cmd.Stderr = &capWriter{w: &out, left: probeOutputCap}, io.Discard
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return out.Bytes(), 0, nil
	case errors.As(err, &ee) && ctx.Err() == nil:
		return out.Bytes(), ee.ExitCode(), nil
	}
	return out.Bytes(), -1, err
}

// capWriter keeps at most left bytes and discards the rest.
type capWriter struct {
	w    io.Writer
	left int
}

// Write keeps what fits and reports every byte written.
func (c *capWriter) Write(b []byte) (int, error) {
	if keep := min(len(b), c.left); keep > 0 {
		if _, err := c.w.Write(b[:keep]); err != nil {
			return 0, err
		}
		c.left -= keep
	}
	return len(b), nil
}
