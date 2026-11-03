package codex

// Purpose: Spawn's guards and the child start, and Cancel's ladder.
// Inputs: a provider.AgentJobSpec from the host shim.
// Outputs: a running job in its own process group, or a typed refusal
// before any child.
// Constraints: cmd.Env is exactly spec.Env.Driver; cmd.Dir is the worktree
// root; argv is [Binary, "app-server"].
// SPORT: providers.agents.codex/ADD (P1-AGT-05).

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/procgroup"
	"github.com/acamarata/cascade/pkg/provider"
)

// cancelConfirmWindow bounds the wait for exit after the kill signal.
const cancelConfirmWindow = 5 * time.Second

// Spawn refuses before any child unless every guard passes, then starts
// `codex app-server` and runs the session in the background.
func (d *Driver) Spawn(_ context.Context, spec provider.AgentJobSpec) (provider.SpawnResult, error) {
	codexDir, err := d.preflight(spec)
	if err != nil {
		return provider.SpawnResult{}, err
	}
	if spec.Effort == "" {
		spec.Effort = d.cfg.Defaults.Effort
	}
	id := spec.JobID
	if id == "" {
		id = newJobID()
	}
	j := newJob(id, spec, codexDir)
	j.sandbox = sandboxFor(spec.ToolPolicy)
	if err := d.claim(j); err != nil {
		return provider.SpawnResult{}, err
	}
	if err := d.start(j); err != nil {
		d.release(id)
		return provider.SpawnResult{}, err
	}
	go d.run(j)
	return provider.SpawnResult{JobID: id, ProcessGroupID: j.pgid}, nil
}

// preflight runs every guard and returns the expected codexHome.
func (d *Driver) preflight(spec provider.AgentJobSpec) (string, error) {
	if !d.cfg.Posture.ProgrammaticEntitlement {
		return "", provider.ErrEntitlement
	}
	if runtime.GOOS == "windows" {
		return "", provider.ErrPlatform
	}
	switch {
	case len(spec.Env.Driver) == 0:
		return "", ErrEmptySpawnEnv
	case spec.WorktreeRoot == "":
		return "", ErrEmptyWorktreeRoot
	case spec.Filter == nil:
		return "", provider.ErrEgressSubstitutionFailed
	}
	if d.cfg.Clock == nil {
		return "", ErrNoClock
	}
	if err := spec.Envelope.Check(spec.WorktreeRoot, d.cfg.Clock.Now()); err != nil {
		return "", err
	}
	if _, err := provider.ResolveToolPolicy(spec.ToolPolicy); err != nil {
		return "", err
	}
	if spec.Effort != "" && !efforts[spec.Effort] {
		return "", ErrInvalidEffort
	}
	codexDir, err := d.checkEnv(spec.Env)
	if err != nil {
		return "", err
	}
	if d.cfg.ContentMatch != nil {
		if err := provider.PreSpawnScan(spec.WorktreeRoot, spec.Scope, d.cfg.ContentMatch); err != nil {
			return "", err
		}
	}
	return codexDir, nil
}

// checkEnv requires every variable on the driver allowlist, HOME equal to
// Env.Home and a non-empty CODEX_HOME, which it returns.
func (d *Driver) checkEnv(env provider.SpawnEnv) (string, error) {
	allow := provider.DriverEnvAllowlist(d.cfg.VendorAuthVar, KeyVarAPIKey)
	vals := make(map[string]string, len(env.Driver))
	for _, kv := range env.Driver {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !slices.Contains(allow, k) {
			return "", cascade.Wrapf(cascade.KindPolicyDenied, ErrEnvNotAllowed, "codex: variable %q is not on the driver allowlist", k)
		}
		vals[k] = v
	}
	if env.Home == "" || vals["HOME"] != env.Home {
		return "", ErrHomeMismatch
	}
	dir := vals[d.cfg.VendorAuthVar]
	if dir == "" {
		return "", ErrCodexHomeMismatch
	}
	return filepath.Clean(dir), nil
}

func (d *Driver) claim(j *job) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if old, ok := d.jobs[j.id]; ok {
		old.mu.Lock()
		live := !old.state.Terminal()
		old.mu.Unlock()
		if live {
			return ErrDuplicateJob
		}
	}
	d.jobs[j.id] = j
	return nil
}

func (d *Driver) release(id provider.AgentJobID) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.jobs, id)
}

// start launches the child in its own process group.
func (d *Driver) start(j *job) error {
	argv := []string{d.cfg.Binary, SubcommandAppServer}
	if err := ValidateLaunch(argv); err != nil {
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // argv is fixed by ValidateLaunch
	cmd.Env = append([]string(nil), j.spec.Env.Driver...)
	cmd.Dir = j.spec.WorktreeRoot
	cmd.SysProcAttr = procgroup.Attr()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, ErrStart, "codex: stdin pipe")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, ErrStart, "codex: stdout pipe")
	}
	if err := cmd.Start(); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, ErrStart, "codex: starting %s: %v", filepath.Base(argv[0]), err)
	}
	pgid, err := procgroup.Lookup(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return cascade.Wrap(cascade.KindUnavailable, ErrStart, "codex: process group lookup")
	}
	j.cmd, j.stdin, j.pgid = cmd, stdin, pgid
	j.reader = newLineReader(stdout)
	return nil
}

// Cancel answers every pending approval with cancel, interrupts the turn,
// closes stdin and walks the signal ladder until the child exited.
func (d *Driver) Cancel(ctx context.Context, id provider.AgentJobID) error {
	j, err := d.lookup(id)
	if err != nil {
		return err
	}
	j.mu.Lock()
	terminal := j.state.Terminal()
	j.mu.Unlock()
	if terminal {
		return nil
	}
	d.stop(j, ErrRunCancelled, decisionCancel)
	grace := d.cfg.Defaults.CancelGrace
	bound := time.NewTimer(2*grace + cancelConfirmWindow)
	defer bound.Stop()
	select {
	case <-j.done:
		return nil
	case <-ctx.Done():
	case <-bound.C:
	}
	return provider.ErrCancelUnconfirmed
}

// stop is the one fail-closed path (cancel, approval timeout, denial):
// mark the job cancelling with cause, answer its pending approvals with
// decision, send turn/interrupt, close stdin and start the signal ladder.
func (d *Driver) stop(j *job, cause error, decision string) {
	j.mu.Lock()
	if j.stopping || j.state.Terminal() {
		j.mu.Unlock()
		return
	}
	j.stopping = true
	j.err = cause
	j.state = provider.AgentRunCancelling
	j.mu.Unlock()
	// The signal ladder starts first: a child that stopped reading stdin
	// can block interrupt and closeStdin on the write lock, and only the
	// group signal unblocks that write.
	go d.ladder(j)
	d.answerPending(j, decision)
	j.interrupt()
	j.closeStdin()
}

// ladder signals the group TERM after one grace and KILL after another.
func (d *Driver) ladder(j *job) {
	grace := d.cfg.Defaults.CancelGrace
	for _, sig := range []procgroup.Sig{procgroup.SigTerm, procgroup.SigKill} {
		t := time.NewTimer(grace)
		select {
		case <-j.done:
			t.Stop()
			return
		case <-t.C:
		}
		_ = procgroup.Signal(j.pgid, sig)
	}
}

// interrupt sends turn/interrupt when a turn is known.
func (j *job) interrupt() {
	j.mu.Lock()
	thread, turn := j.journal.ThreadID, j.journal.TurnID
	if thread == "" || turn == "" {
		j.mu.Unlock()
		return
	}
	j.nextID++
	id := j.nextID
	j.outstanding[idKey(id)] = true
	j.mu.Unlock()
	if line, err := encodeRequest(id, "turn/interrupt", map[string]any{"threadId": thread, "turnId": turn}); err == nil {
		_ = j.write(line)
	}
}

// cryptoRead is crypto/rand's reader (aliased so the rand.* lint that
// targets math/rand does not match it).
var cryptoRead = cryptorand.Read

func newJobID() provider.AgentJobID {
	var b [12]byte
	_, _ = cryptoRead(b[:])
	return provider.AgentJobID("codex-" + hex.EncodeToString(b[:]))
}
